package botapp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// telegramCallbackLimit is the Bot API's ceiling on callback_data, in bytes.
// A longer value is refused with BUTTON_DATA_INVALID and the whole message —
// not just the button — fails to send or edit.
const telegramCallbackLimit = 64

// Placeholder widths for the values a callback carries. An identifier is a
// UUID unless the table below knows better, which is the conservative default:
// almost every argument in a callback is a database identifier.
const (
	uuidWidth    = 36
	integerWidth = 12
)

// boundedValues names the callback arguments that are not identifiers, with
// the widest value each can take. A name missing here is assumed to be a UUID.
var boundedValues = map[string]int{
	// Payment adapters are short codes — `telegram_stars` is the longest at 14
	// — and currencies are ISO 4217.
	"Provider": 14, "provider": 14, "Currency": 3, "currency": 3,
	// Quiet-hour windows and device positions are small integers.
	"index": 3, "start": 2, "end": 2, "days": 3, "slot": 3, "Slot": 3,
	// Closed sets of short words.
	"funding": 12, "platform": 12, "operation": 10, "kind": 12, "locale": 4,
	"enabled": 3, "state": 3, "action": 20, "label": 20,
	"choice.Provider": 14, "choice.Currency": 3, "amount": integerWidth,
	"option": 12,
}

// callbackEvaluator turns a callback expression into the widest string it can
// produce, with every value it cannot know replaced by a placeholder of the
// width that value can have.
type callbackEvaluator struct {
	constants map[string]string
	functions map[string]*ast.FuncDecl
	depth     int
}

func newCallbackEvaluator(files []sourceFile) *callbackEvaluator {
	evaluator := &callbackEvaluator{constants: map[string]string{}, functions: map[string]*ast.FuncDecl{}}
	for _, source := range files {
		for _, declaration := range source.file.Decls {
			switch typed := declaration.(type) {
			case *ast.GenDecl:
				if typed.Tok != token.CONST {
					continue
				}
				for _, spec := range typed.Specs {
					values := spec.(*ast.ValueSpec)
					for index, name := range values.Names {
						if index >= len(values.Values) {
							continue
						}
						if literal, ok := values.Values[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
							evaluator.constants[name.Name], _ = strconv.Unquote(literal.Value)
						}
					}
				}
			case *ast.FuncDecl:
				if typed.Recv == nil {
					evaluator.functions[typed.Name.Name] = typed
				}
			}
		}
	}
	return evaluator
}

func placeholder(width int) string { return strings.Repeat("x", width) }

func widthOf(name string) int {
	if width, known := boundedValues[name]; known {
		return width
	}
	return uuidWidth
}

// eval reports the widest value of an expression and whether its start is a
// literal — which is what makes the action name in it knowable.
func (evaluator *callbackEvaluator) eval(expression ast.Expr, environment map[string]string) (string, bool) {
	switch typed := expression.(type) {
	case *ast.BasicLit:
		if typed.Kind == token.STRING {
			value, _ := strconv.Unquote(typed.Value)
			return value, true
		}
		return placeholder(integerWidth), false
	case *ast.ParenExpr:
		return evaluator.eval(typed.X, environment)
	case *ast.Ident:
		if bound, ok := environment[typed.Name]; ok {
			return bound, true
		}
		if constant, ok := evaluator.constants[typed.Name]; ok {
			return constant, true
		}
		return placeholder(widthOf(typed.Name)), false
	case *ast.SelectorExpr:
		if base, ok := typed.X.(*ast.Ident); ok {
			if width, known := boundedValues[base.Name+"."+typed.Sel.Name]; known {
				return placeholder(width), false
			}
		}
		return placeholder(widthOf(typed.Sel.Name)), false
	case *ast.BinaryExpr:
		left, literal := evaluator.eval(typed.X, environment)
		right, _ := evaluator.eval(typed.Y, environment)
		return left + right, literal
	case *ast.CallExpr:
		return evaluator.evalCall(typed, environment)
	default:
		return placeholder(uuidWidth), false
	}
}

func (evaluator *callbackEvaluator) evalCall(call *ast.CallExpr, environment map[string]string) (string, bool) {
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, _ := function.X.(*ast.Ident)
		switch {
		case pkg != nil && pkg.Name == "fmt" && function.Sel.Name == "Sprintf" && len(call.Args) > 0:
			format, literal := evaluator.eval(call.Args[0], environment)
			argument := 1
			rendered := formatVerb.ReplaceAllStringFunc(format, func(verb string) string {
				if verb == "%%" {
					return "%"
				}
				defer func() { argument++ }()
				if strings.HasSuffix(verb, "d") {
					// A named small integer — a device position — is as wide
					// as its name says; any other integer gets the full width.
					if argument < len(call.Args) {
						if name, ok := call.Args[argument].(*ast.Ident); ok {
							if width, known := boundedValues[name.Name]; known {
								return placeholder(width)
							}
						}
					}
					return placeholder(integerWidth)
				}
				if argument < len(call.Args) {
					value, _ := evaluator.eval(call.Args[argument], environment)
					return value
				}
				return placeholder(uuidWidth)
			})
			return rendered, literal
		case pkg != nil && pkg.Name == "strconv":
			if len(call.Args) > 0 {
				if name, ok := call.Args[0].(*ast.Ident); ok {
					if width, known := boundedValues[name.Name]; known {
						return placeholder(width), false
					}
				}
			}
			return placeholder(integerWidth), false
		case pkg != nil && pkg.Name == "strings" && len(call.Args) > 0:
			return evaluator.eval(call.Args[0], environment)
		}
		return placeholder(uuidWidth), false
	case *ast.Ident:
		declaration, local := evaluator.functions[function.Name]
		if !local || evaluator.depth > 4 {
			return placeholder(widthOf(function.Name)), false
		}
		// A package helper that builds a callback is evaluated with its
		// parameters bound, and its widest return is the answer.
		bound := map[string]string{}
		position := 0
		for _, field := range declaration.Type.Params.List {
			for _, name := range field.Names {
				if position < len(call.Args) {
					bound[name.Name], _ = evaluator.eval(call.Args[position], environment)
				}
				position++
			}
		}
		evaluator.depth++
		defer func() { evaluator.depth-- }()
		widest, literal := "", false
		ast.Inspect(declaration.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.ReturnStmt)
			if !ok || len(statement.Results) == 0 {
				return true
			}
			value, isLiteral := evaluator.eval(statement.Results[0], bound)
			if len(value) > len(widest) {
				widest, literal = value, isLiteral
			}
			return true
		})
		return widest, literal
	}
	return placeholder(uuidWidth), false
}

// renderedButton is one button found in the source.
type renderedButton struct {
	constructor string
	data        string
	literal     bool
	position    string
}

func renderedButtons(t *testing.T) []renderedButton {
	t.Helper()
	fileSet, files := packageSources(t)
	evaluator := newCallbackEvaluator(files)
	buttons := make([]renderedButton, 0, 256)
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || len(call.Args) != 2 {
				return true
			}
			prefix := ""
			switch name.Name {
			case "actionButton":
				prefix = actionPrefix
			case "callbackButton":
				prefix = callbackPrefix
			default:
				return true
			}
			data, literal := evaluator.eval(call.Args[1], nil)
			position := fileSet.Position(call.Pos())
			buttons = append(buttons, renderedButton{
				constructor: name.Name, data: prefix + data, literal: literal,
				position: filepath.Base(position.Filename) + ":" + strconv.Itoa(position.Line),
			})
			return true
		})
	}
	if len(buttons) < 50 {
		t.Fatalf("found only %d buttons; the source scan is not reading the package", len(buttons))
	}
	return buttons
}

// TestEveryCallbackFitsTelegramsLimit fails on any button whose callback could
// exceed 64 bytes once its identifiers are filled in. Telegram refuses the
// whole message for one such button, which is how a shop screen with a long
// recipient name used to stay on "Refreshing your data…" for good.
func TestEveryCallbackFitsTelegramsLimit(t *testing.T) {
	t.Parallel()
	for _, button := range renderedButtons(t) {
		if len(button.data) > telegramCallbackLimit {
			t.Errorf("%s: %s builds %q, %d bytes with identifiers filled in; Telegram accepts %d",
				button.position, button.constructor, button.data, len(button.data), telegramCallbackLimit)
		}
	}
}

// TestTheLimitCheckSeesALongCallback keeps the scan honest: the evaluator has
// to see through a helper and a format string, or the test above passes by
// reading nothing.
func TestTheLimitCheckSeesALongCallback(t *testing.T) {
	t.Parallel()
	_, files := packageSources(t)
	evaluator := newCallbackEvaluator(files)
	expression, err := parser.ParseExpr(`fmt.Sprintf("shop-buy:%s:%s", product.ID, recipient)`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if value, _ := evaluator.eval(expression, nil); len(actionPrefix+value) <= telegramCallbackLimit {
		t.Fatalf("a product and a recipient evaluated to %q, which fits; the scan is blind", value)
	}
	helper, err := parser.ParseExpr(`renewCallback("renew-settings", subscriptionID)`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if value, _ := evaluator.eval(helper, nil); value != "renew-settings:"+placeholder(uuidWidth) {
		t.Fatalf("the helper evaluated to %q; its widest return was not followed", value)
	}
}

// actionName is the part of an action callback the dispatcher switches on.
func actionName(data string) string {
	return strings.SplitN(strings.TrimPrefix(data, actionPrefix), ":", 2)[0]
}

// TestEveryButtonReachesAHandler fails on a rendered button nothing handles.
// Such a tap answers "Action was not completed", or for a route, opens the main
// menu instead of the screen the button named.
func TestEveryButtonReachesAHandler(t *testing.T) {
	t.Parallel()
	for _, button := range renderedButtons(t) {
		if !button.literal {
			continue
		}
		switch button.constructor {
		case "callbackButton":
			route := strings.TrimPrefix(button.data, callbackPrefix)
			if !knownRoute(route) {
				t.Errorf("%s: route %q is rendered but unknown to the router", button.position, route)
			}
		case "actionButton":
			action := actionName(button.data)
			if !commerceActions[action] && !legacyActions[action] {
				t.Errorf("%s: action %q is rendered but no handler owns it", button.position, action)
			}
		}
	}
}

// TestEveryHandledActionHasAButton is the other direction: an action with a
// handler and no button is either dead code or a screen somebody forgot to
// link, and both are worth a failing test rather than a review comment.
//
// Some buttons take their action from a table or a variable — the ticket
// actions, the device confirmation — so an action counts as rendered when any
// string in the package spells it out as a callback, outside the `case` labels
// and map keys that only declare a handler.
func TestEveryHandledActionHasAButton(t *testing.T) {
	t.Parallel()
	rendered := map[string]bool{}
	for _, button := range renderedButtons(t) {
		if button.constructor == "actionButton" && button.literal {
			rendered[actionName(button.data)] = true
		}
	}
	_, files := packageSources(t)
	declarations := map[*ast.BasicLit]bool{}
	literals := make([]string, 0, 1024)
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CaseClause:
				for _, label := range typed.List {
					if literal, ok := label.(*ast.BasicLit); ok {
						declarations[literal] = true
					}
				}
			case *ast.KeyValueExpr:
				if literal, ok := typed.Key.(*ast.BasicLit); ok {
					declarations[literal] = true
				}
			case *ast.BasicLit:
				if typed.Kind == token.STRING && !declarations[typed] {
					value, _ := strconv.Unquote(typed.Value)
					literals = append(literals, value)
				}
			}
			return true
		})
	}
	spelled := func(action string) bool {
		for _, literal := range literals {
			if literal == action || strings.HasPrefix(literal, action+":") {
				return true
			}
		}
		return false
	}
	missing := make([]string, 0)
	for _, owned := range []map[string]bool{commerceActions, legacyActions} {
		for action := range owned {
			if !rendered[action] && !spelled(action) {
				missing = append(missing, action)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("actions with a handler and no button: %v", missing)
	}
}
