// Package linkcheck inspects links and attachment metadata in customer
// material.
//
// A support ticket is a channel an attacker controls, and the two things they
// put in it are a link and a file. This package looks at both, and its shape
// comes from one decision: nothing here fetches anything unless an owner
// configured a tool to.
//
// The structural checks need no network. A punycode homograph, an IP-literal
// host, a credential in the userinfo, an anchor whose text disagrees with its
// target, a double extension on an attachment — all of these are visible in the
// string, and finding them costs nothing and reveals nothing. They run always.
//
// Reputation checks need a third party, and sending an installation's customer
// links to one is a disclosure. So a reputation tool is off until an owner
// names it, and Omniflow never picks a default: an integration that quietly
// appears is a data-sharing decision somebody did not make.
//
// Nothing here blocks a message or punishes anybody. It produces findings an
// operator reads next to the ticket, because "this link is suspicious" is a
// judgement, and the person answering the customer is better placed to make it
// than a pattern is.
package linkcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Severity is how much attention a finding deserves.
//
// Three levels, because an operator triaging a queue reads a colour, and a
// finer scale would be a number they have to interpret.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityDanger  = "danger"
)

// Finding codes.
const (
	FindingPunycode        = "punycode_host"
	FindingLookalike       = "lookalike_domain"
	FindingIPLiteral       = "ip_literal_host"
	FindingCredentialInURL = "credential_in_url"
	FindingAnchorMismatch  = "anchor_mismatch"
	FindingShortener       = "url_shortener"
	FindingUnusualPort     = "unusual_port"
	FindingPlaintext       = "plaintext_scheme"
	FindingExecutable      = "executable_attachment"
	FindingDoubleExtension = "double_extension"
	FindingTypeMismatch    = "declared_type_mismatch"
	FindingOversizeFile    = "oversize_attachment"
	FindingReputation      = "reputation_flagged"
	FindingCheckerFailed   = "reputation_check_failed"
)

// Finding is one thing worth an operator's attention.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Subject is the link or filename the finding is about, so a ticket with
	// nine links produces findings an operator can attribute.
	Subject string `json:"subject"`
	// Detail is written for the operator, and it says what was observed rather
	// than what to do — the decision is theirs.
	Detail string `json:"detail"`
	// Source names what produced it: "structure" for the local checks, or the
	// configured tool's name. An operator seeing a reputation verdict should
	// know whose verdict it is.
	Source string `json:"source"`
}

// Link is one link found in customer material.
type Link struct {
	// URL is the target.
	URL string
	// Text is the visible anchor text, when the material had markup. An anchor
	// reading "your invoice" pointing somewhere else is the oldest trick here,
	// and it is invisible without both halves.
	Text string
}

// Attachment is one file's metadata.
//
// Metadata only. This package never reads a file's contents: an attachment is
// hostile input, and a parser is an attack surface. What the name and the
// declared type disagree about is enough to warrant a look.
type Attachment struct {
	Filename string
	// DeclaredType is the MIME type the sender's client claimed.
	DeclaredType string
	SizeBytes    int64
}

// Reputation is an owner-configured external check.
//
// It is an interface with a name because an operator reading a verdict needs to
// know whose verdict it is, and because an installation that has configured
// none must behave identically to one that has — minus the verdicts.
type Reputation interface {
	// Name identifies the tool in findings and in the audit trail.
	Name() string
	// Check returns findings for one URL. It is called only for links that
	// passed the structural checks, so a tool is not spent on a link already
	// known to be malformed.
	Check(ctx context.Context, target string) ([]Finding, error)
}

// ErrNoChecker reports a reputation check requested on an installation that has
// configured none. It is a normal state, not a failure.
var ErrNoChecker = errors.New("no reputation tool is configured")

// Checker inspects material.
type Checker struct {
	// reputation is nil until an owner configures one. Omniflow never picks a
	// default, because an integration that quietly appears is a data-sharing
	// decision somebody did not make.
	reputation Reputation
	timeout    time.Duration
	// trusted are the installation's own hosts, so its own links do not warn.
	trusted map[string]bool
}

// Options configures the checker.
type Options struct {
	Reputation Reputation
	Timeout    time.Duration
	// TrustedHosts are the installation's own domains.
	TrustedHosts []string
}

// DefaultTimeout bounds one reputation lookup. An operator waiting on a ticket
// needs a verdict or a shrug, not a hung request.
const DefaultTimeout = 5 * time.Second

// New builds a checker. A checker with no reputation tool is fully functional:
// the structural checks are the ones that run always.
func New(options Options) *Checker {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	trusted := make(map[string]bool, len(options.TrustedHosts))
	for _, host := range options.TrustedHosts {
		trusted[strings.ToLower(strings.TrimSpace(host))] = true
	}
	return &Checker{reputation: options.Reputation, timeout: timeout, trusted: trusted}
}

// ReputationConfigured reports whether an external check is available, so a
// surface can say "no reputation tool is configured" rather than implying a
// clean verdict.
func (checker *Checker) ReputationConfigured() bool { return checker.reputation != nil }

// Inspect examines links and attachments.
//
// Structural findings always come back. Reputation findings come back only if
// an owner configured a tool, and a tool that fails produces a finding saying
// so rather than silence — silence reads as "clean".
func (checker *Checker) Inspect(
	ctx context.Context, links []Link, attachments []Attachment,
) []Finding {
	findings := make([]Finding, 0, 8)

	for _, link := range links {
		structural := checker.inspectLink(link)
		findings = append(findings, structural...)
		if checker.reputation == nil || len(structural) > 0 {
			// A link already known to be malformed does not need a third party's
			// opinion, and not sending it is one fewer disclosure.
			continue
		}
		findings = append(findings, checker.reputationFor(ctx, link.URL)...)
	}

	for _, attachment := range attachments {
		findings = append(findings, inspectAttachment(attachment)...)
	}

	sort.SliceStable(findings, func(left, right int) bool {
		return severityRank(findings[left].Severity) < severityRank(findings[right].Severity)
	})
	return findings
}

func (checker *Checker) reputationFor(ctx context.Context, target string) []Finding {
	lookupCtx, cancel := context.WithTimeout(ctx, checker.timeout)
	defer cancel()

	verdicts, err := checker.reputation.Check(lookupCtx, target)
	if err != nil {
		// Silence would read as "clean". An operator deciding whether to trust a
		// link needs to know the check did not happen.
		return []Finding{{
			Code: FindingCheckerFailed, Severity: SeverityInfo, Subject: target,
			Detail: "The reputation check did not complete, so this link is unverified.",
			Source: checker.reputation.Name(),
		}}
	}
	for index := range verdicts {
		verdicts[index].Source = checker.reputation.Name()
		if verdicts[index].Code == "" {
			verdicts[index].Code = FindingReputation
		}
		if verdicts[index].Subject == "" {
			verdicts[index].Subject = target
		}
	}
	return verdicts
}

// shorteners hide a destination behind a domain that says nothing. They are not
// malicious in themselves, which is why this is a warning and not a verdict.
var shorteners = map[string]bool{
	"bit.ly": true, "tinyurl.com": true, "t.co": true, "goo.gl": true,
	"ow.ly": true, "is.gd": true, "buff.ly": true, "rb.gy": true,
	"cutt.ly": true, "shorturl.at": true, "rebrand.ly": true,
}

// lookalikeCharacters are the substitutions used to build a domain that reads
// as another one at a glance.
var lookalikeCharacters = strings.NewReplacer(
	"0", "o", "1", "l", "3", "e", "4", "a", "5", "s", "7", "t", "-", "",
)

// suspiciousBrands are the words a phishing domain borrows. The list is short
// and installation-agnostic on purpose: it catches the generic attempts, and
// the trusted-host list is how an installation says what its own domains are.
var suspiciousBrands = []string{
	"telegram", "remnawave", "omniflow", "paypal", "binance", "support", "billing",
}

func (checker *Checker) inspectLink(link Link) []Finding {
	findings := make([]Finding, 0, 3)
	target := strings.TrimSpace(link.URL)
	if target == "" {
		return findings
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return findings
	}
	host := strings.ToLower(parsed.Hostname())
	if checker.trusted[host] {
		return findings
	}

	if parsed.User != nil {
		// A userinfo section is how a link is made to look like it points at one
		// host while pointing at another, and it is also how a credential ends
		// up in a browser history.
		findings = append(findings, Finding{
			Code: FindingCredentialInURL, Severity: SeverityDanger, Subject: target,
			Detail: "The link contains a username or password before the host, which " +
				"hides where it actually points.",
			Source: "structure",
		})
	}
	if strings.HasPrefix(host, "xn--") || strings.Contains(host, ".xn--") {
		findings = append(findings, Finding{
			Code: FindingPunycode, Severity: SeverityDanger, Subject: target,
			Detail: "The host is punycode-encoded, so it may display as a different " +
				"domain than the one it resolves to.",
			Source: "structure",
		})
	}
	if net.ParseIP(host) != nil {
		findings = append(findings, Finding{
			Code: FindingIPLiteral, Severity: SeverityWarning, Subject: target,
			Detail: "The link points at a bare IP address rather than a domain name.",
			Source: "structure",
		})
	}
	if shorteners[host] {
		findings = append(findings, Finding{
			Code: FindingShortener, Severity: SeverityWarning, Subject: target,
			Detail: "The link is shortened, so its destination is not visible until it " +
				"is opened.",
			Source: "structure",
		})
	}
	if parsed.Scheme == "http" {
		findings = append(findings, Finding{
			Code: FindingPlaintext, Severity: SeverityInfo, Subject: target,
			Detail: "The link is unencrypted http.", Source: "structure",
		})
	}
	if port := parsed.Port(); port != "" && port != "80" && port != "443" {
		findings = append(findings, Finding{
			Code: FindingUnusualPort, Severity: SeverityInfo, Subject: target,
			Detail: "The link uses port " + port + " rather than a standard web port.",
			Source: "structure",
		})
	}
	if brand := lookalikeBrand(host, checker.trusted); brand != "" {
		findings = append(findings, Finding{
			Code: FindingLookalike, Severity: SeverityDanger, Subject: target,
			Detail: fmt.Sprintf("The host resembles %q but is not a domain this "+
				"installation recognises.", brand),
			Source: "structure",
		})
	}
	if mismatch := anchorMismatch(link); mismatch != "" {
		findings = append(findings, Finding{
			Code: FindingAnchorMismatch, Severity: SeverityDanger, Subject: target,
			Detail: mismatch, Source: "structure",
		})
	}
	return findings
}

// lookalikeBrand reports a brand a host is imitating without being it.
func lookalikeBrand(host string, trusted map[string]bool) string {
	normalised := lookalikeCharacters.Replace(host)
	for _, brand := range suspiciousBrands {
		if !strings.Contains(normalised, brand) {
			continue
		}
		// Containing the brand is only suspicious when the host is not actually
		// the brand's own domain or one the installation trusts.
		if trusted[host] || host == brand+".com" || host == brand+".org" ||
			strings.HasSuffix(host, "."+brand+".com") {
			continue
		}
		// A hyphen-or-digit substitution is what makes it a lookalike rather
		// than a legitimate subdomain of somebody else's site.
		if normalised != host || strings.Count(host, "-") > 0 {
			return brand
		}
	}
	return ""
}

// hostPattern finds something that reads as a hostname inside anchor text.
var hostPattern = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,})\b`)

// anchorMismatch reports anchor text that names a different host than the link
// points at. It is the oldest trick here and invisible without both halves.
func anchorMismatch(link Link) string {
	text := strings.TrimSpace(link.Text)
	if text == "" {
		return ""
	}
	parsed, err := url.Parse(link.URL)
	if err != nil {
		return ""
	}
	actual := strings.ToLower(parsed.Hostname())

	for _, match := range hostPattern.FindAllStringSubmatch(text, -1) {
		claimed := strings.ToLower(match[1])
		if claimed == actual || strings.HasSuffix(actual, "."+claimed) {
			continue
		}
		return fmt.Sprintf("The link text says %q but the link points at %q.", claimed, actual)
	}
	return ""
}

// executableExtensions are the ones that run when opened. The list covers what
// a customer might plausibly attach; it is not a complete inventory of every
// executable format, and it does not need to be — an operator seeing a warning
// on a .exe is not helped by the list also covering .scf.
var executableExtensions = map[string]bool{
	".exe": true, ".scr": true, ".bat": true, ".cmd": true, ".com": true,
	".msi": true, ".ps1": true, ".vbs": true, ".js": true, ".jar": true,
	".apk": true, ".dmg": true, ".sh": true, ".lnk": true, ".hta": true,
}

// documentExtensions map to the type a well-behaved client declares, so a
// disagreement is visible without opening the file.
var documentExtensions = map[string]string{
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg",
	".jpeg": "image/jpeg", ".gif": "image/gif", ".txt": "text/plain",
	".csv": "text/csv", ".zip": "application/zip",
}

// MaxReasonableAttachment is the size past which an attachment is worth
// mentioning. It is not a limit — refusing is the upload path's job — it is the
// point at which "why did they send this?" becomes a fair question.
const MaxReasonableAttachment int64 = 25 << 20

func inspectAttachment(attachment Attachment) []Finding {
	findings := make([]Finding, 0, 2)
	name := strings.TrimSpace(attachment.Filename)
	if name == "" {
		return findings
	}
	lowered := strings.ToLower(name)
	extension := path.Ext(lowered)

	if executableExtensions[extension] {
		findings = append(findings, Finding{
			Code: FindingExecutable, Severity: SeverityDanger, Subject: name,
			Detail: "The attachment is an executable file type.", Source: "structure",
		})
	}
	// A double extension is how an executable is dressed as a document. The
	// check is on the inner extension being a document type, because
	// "report.2026.pdf" is ordinary and "invoice.pdf.exe" is not.
	if inner := path.Ext(strings.TrimSuffix(lowered, extension)); inner != "" {
		if _, document := documentExtensions[inner]; document && extension != inner {
			findings = append(findings, Finding{
				Code: FindingDoubleExtension, Severity: SeverityDanger, Subject: name,
				Detail: fmt.Sprintf("The filename ends in %s but has %s before it, which "+
					"makes an executable look like a document.", extension, inner),
				Source: "structure",
			})
		}
	}
	if expected, known := documentExtensions[extension]; known {
		declared := strings.ToLower(strings.TrimSpace(attachment.DeclaredType))
		if declared != "" && declared != expected {
			findings = append(findings, Finding{
				Code: FindingTypeMismatch, Severity: SeverityWarning, Subject: name,
				Detail: fmt.Sprintf("The name says %s but the declared type is %q.",
					extension, declared),
				Source: "structure",
			})
		}
	}
	if attachment.SizeBytes > MaxReasonableAttachment {
		findings = append(findings, Finding{
			Code: FindingOversizeFile, Severity: SeverityInfo, Subject: name,
			Detail: fmt.Sprintf("The attachment is %d MB.", attachment.SizeBytes>>20),
			Source: "structure",
		})
	}
	return findings
}

func severityRank(severity string) int {
	switch severity {
	case SeverityDanger:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// linkPattern finds bare URLs in plain text, which is how they arrive in a
// Telegram message.
var linkPattern = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// LinksIn extracts the links from plain text.
//
// It exists so a caller with a plain-text ticket body gets the same checks as
// one with markup, without each caller writing its own regular expression and
// each getting it slightly wrong.
func LinksIn(text string) []Link {
	matches := linkPattern.FindAllString(text, -1)
	links := make([]Link, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		// Trailing punctuation belongs to the sentence, not to the link.
		trimmed := strings.TrimRight(match, ".,;:!?")
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		links = append(links, Link{URL: trimmed})
	}
	return links
}
