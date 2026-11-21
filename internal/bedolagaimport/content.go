package bedolagaimport

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Page is one of Bedolaga's documents: the privacy policy, the public offer,
// a section of the service rules, or one FAQ question.
type Page struct {
	// Kind is PagePrivacy, PageOffer, PageRules, or PageFAQ.
	Kind     string
	SourceID int64
	Language string
	// Title is the section's or question's own title; the policy and the
	// offer have none.
	Title     string
	Body      string
	Enabled   bool
	Order     int64
	UpdatedAt time.Time
}

// Bedolaga's document kinds.
const (
	PagePrivacy = "privacy"
	PageOffer   = "offer"
	PageRules   = "rules"
	PageFAQ     = "faq"
)

// Ticket is a Bedolaga support ticket with its whole conversation.
type Ticket struct {
	ID        int64
	UserID    int64
	Title     string
	Status    string
	Priority  string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	Messages  []TicketMessage
}

// TicketMessage is one message in a Bedolaga ticket. The files themselves
// live in the previous bot's chats, so only what they were is carried.
type TicketMessage struct {
	ID        int64
	FromAdmin bool
	Text      string
	HasMedia  bool
	MediaType string
	Caption   string
	CreatedAt time.Time
}

// Consent is a customer's acceptance of a Bedolaga legal document.
type Consent struct {
	ID         int64
	UserID     int64
	Document   string
	Source     string
	AcceptedAt time.Time
}

// Codes of the content decisions.
const (
	CodeEmptyPage          = "empty_page"
	CodePageLocale         = "page_locale_unsupported"
	CodePageTruncated      = "page_truncated"
	CodeEmptyTicket        = "empty_ticket"
	CodeConsentDocument    = "consent_document_unknown"
	CodeMessageTruncated   = "message_truncated"
	CodeSupportTopicChosen = "support_topic_pending"
)

// PageDecision carries one document into one language of an Omniflow page.
// A page an operator already wrote in Omniflow is left as it is.
type PageDecision struct {
	Slug      string
	Kind      string
	Locale    string
	Title     string
	Body      string
	Published bool
	SortOrder int
	SourceIDs []int64
	Action    Action
	Codes     []string
	Warnings  []string
}

// TicketDecision carries one ticket and its messages over as history.
type TicketDecision struct {
	Source     Ticket
	Action     Action
	Codes      []string
	Warnings   []string
	CustomerID string
	Status     string
	Priority   string
	Subject    string
}

// ConsentDecision carries one acceptance as a consent record.
type ConsentDecision struct {
	Source     Consent
	Action     Action
	Codes      []string
	CustomerID string
	Purpose    string
}

// pageTarget is where a Bedolaga document kind lands in Omniflow.
type pageTarget struct {
	slug, kind string
	titles     map[string]string
	sortOrder  int
}

var pageTargets = map[string]pageTarget{
	PagePrivacy: {"privacy", "privacy", map[string]string{"ru": "Политика конфиденциальности", "en": "Privacy policy"}, 30},
	PageOffer:   {"offer", "offer", map[string]string{"ru": "Публичная оферта", "en": "Public offer"}, 20},
	PageRules:   {"terms", "terms", map[string]string{"ru": "Правила сервиса", "en": "Terms of service"}, 10},
	PageFAQ:     {"faq", "faq", map[string]string{"ru": "Частые вопросы", "en": "FAQ"}, 0},
}

// Omniflow's limits on a page, from info_page_localizations.
const (
	maxPageBody  = 40000
	maxPageTitle = 200
	// maxMessageBody and maxSubject are support_messages.body and
	// support_tickets.subject.
	maxMessageBody = 4000
	maxSubject     = 120
)

// LegacyTicketKey is the dedupe key of a carried ticket message, which is also
// how a repeated run finds the tickets already carried.
func LegacyTicketKey(ticketID, messageID int64) string {
	return "bedolaga:ticket:" + strconv.FormatInt(ticketID, 10) + ":message:" + strconv.FormatInt(messageID, 10)
}

// LegacyConsentKey is the request ID of a carried consent record.
func LegacyConsentKey(consentID int64) string {
	return "bedolaga:legal_consent:" + strconv.FormatInt(consentID, 10)
}

func decidePages(plan *Plan, source Source, target Target) {
	type group struct {
		kind, locale string
		pages        []Page
	}
	groups := map[string]*group{}
	for _, page := range source.Pages {
		locale := strings.ToLower(strings.TrimSpace(page.Language))
		key := page.Kind + ":" + locale
		if groups[key] == nil {
			groups[key] = &group{kind: page.Kind, locale: locale}
		}
		groups[key].pages = append(groups[key].pages, page)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := groups[key]
		destination, known := pageTargets[entry.kind]
		if !known {
			continue
		}
		decision := PageDecision{
			Slug: destination.slug, Kind: destination.kind, Locale: entry.locale,
			Title: destination.titles[entry.locale], SortOrder: destination.sortOrder,
		}
		body, sources, published := pageBody(entry.kind, entry.pages)
		decision.SourceIDs = sources
		decision.Published = published
		if entry.kind == PageRules {
			// One rules section keeps its own title, as Bedolaga showed it.
			if enabled := enabledPages(entry.pages); len(enabled) == 1 && strings.TrimSpace(enabled[0].Title) != "" {
				decision.Title = strings.TrimSpace(enabled[0].Title)
			}
		}
		decision.Title = truncateRunes(decision.Title, maxPageTitle)
		if utf8.RuneCountInString(body) > maxPageBody {
			body = truncateRunes(body, maxPageBody)
			decision.Warnings = append(decision.Warnings, CodePageTruncated)
		}
		decision.Body = body
		switch {
		case entry.locale != "ru" && entry.locale != "en":
			decision.Action, decision.Codes = ActionSkip, []string{CodePageLocale}
		case strings.TrimSpace(body) == "" || decision.Title == "":
			decision.Action, decision.Codes = ActionSkip, []string{CodeEmptyPage}
		case target.InfoPageLocales[decision.Slug+":"+decision.Locale]:
			// The page already has this language in Omniflow, written there
			// or carried before; the operator's version stands.
			decision.Action = ActionUnchanged
		default:
			decision.Action = ActionCreate
		}
		plan.Pages = append(plan.Pages, decision)
	}
}

func enabledPages(pages []Page) []Page {
	enabled := make([]Page, 0, len(pages))
	for _, page := range pages {
		if page.Enabled && strings.TrimSpace(page.Body) != "" {
			enabled = append(enabled, page)
		}
	}
	sort.Slice(enabled, func(i, j int) bool {
		if enabled[i].Order != enabled[j].Order {
			return enabled[i].Order < enabled[j].Order
		}
		return enabled[i].SourceID < enabled[j].SourceID
	})
	return enabled
}

// pageBody assembles one Omniflow page from the Bedolaga rows of one kind and
// language: the enabled one for the policy and the offer, every enabled
// section in order for the rules, every enabled question for the FAQ. A
// document Bedolaga had switched off is still carried, unpublished, so its
// text is not lost.
func pageBody(kind string, pages []Page) (string, []int64, bool) {
	chosen := enabledPages(pages)
	published := len(chosen) > 0
	if !published {
		// Nothing enabled: the newest text with content, kept as a draft.
		var latest *Page
		for index := range pages {
			page := &pages[index]
			if strings.TrimSpace(page.Body) == "" {
				continue
			}
			if latest == nil || page.UpdatedAt.After(latest.UpdatedAt) {
				latest = page
			}
		}
		if latest == nil {
			return "", nil, false
		}
		chosen = []Page{*latest}
	}
	sections := make([]string, 0, len(chosen))
	sources := make([]int64, 0, len(chosen))
	for _, page := range chosen {
		sources = append(sources, page.SourceID)
		text := PageText(page.Body)
		withHeading := kind == PageFAQ || (kind == PageRules && len(chosen) > 1)
		if title := oneLine(page.Title); withHeading && title != "" {
			text = "## " + title + "\n\n" + text
		}
		sections = append(sections, text)
	}
	return strings.Join(sections, "\n\n"), sources, published
}

var (
	anchorPattern    = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["'](https://[^"'\s]+)["'][^>]*>(.*?)</a>`)
	breakPattern     = regexp.MustCompile(`(?i)<br\s*/?>`)
	blockEndPattern  = regexp.MustCompile(`(?i)</(p|div|li|h[1-6]|blockquote|pre)>`)
	tagPattern       = regexp.MustCompile(`<[^>]+>`)
	numberedPattern  = regexp.MustCompile(`^\d+(\.\d+)*\.?\s+\S`)
	manyBlankPattern = regexp.MustCompile(`\n{3,}`)
)

// PageText turns Bedolaga's document text, which is Telegram HTML or plain
// text, into the small syntax Omniflow pages are written in: https links
// become [text](url), other markup is dropped, and a short numbered line
// standing alone between blank lines becomes a heading, which is how the
// policy and the offer mark their sections.
func PageText(source string) string {
	text := strings.ReplaceAll(source, "\r\n", "\n")
	text = anchorPattern.ReplaceAllStringFunc(text, func(anchor string) string {
		parts := anchorPattern.FindStringSubmatch(anchor)
		label := strings.TrimSpace(tagPattern.ReplaceAllString(parts[2], ""))
		label = strings.NewReplacer("[", "(", "]", ")").Replace(html.UnescapeString(label))
		if label == "" {
			label = parts[1]
		}
		return "[" + label + "](" + parts[1] + ")"
	})
	text = breakPattern.ReplaceAllString(text, "\n")
	text = blockEndPattern.ReplaceAllString(text, "\n\n")
	text = tagPattern.ReplaceAllString(text, "")
	text = html.UnescapeString(text)

	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	blank := func(index int) bool { return index < 0 || index >= len(lines) || strings.TrimSpace(lines[index]) == "" }
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if numberedPattern.MatchString(trimmed) && utf8.RuneCountInString(trimmed) <= 90 &&
			!strings.HasSuffix(trimmed, ".") && !strings.HasSuffix(trimmed, ";") &&
			!strings.HasSuffix(trimmed, ",") && blank(index-1) && blank(index+1) {
			lines[index] = "## " + trimmed
		}
	}
	text = strings.Join(lines, "\n")
	text = manyBlankPattern.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func decideTickets(plan *Plan, source Source, target Target, userIndex map[int64]int) {
	tickets := append([]Ticket(nil), source.Tickets...)
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })
	for _, ticket := range tickets {
		decision := TicketDecision{
			Source: ticket, Status: ticketStatus(ticket.Status), Priority: ticketPriority(ticket.Priority),
			Subject: truncateRunes(oneLine(ticket.Title), maxSubject),
		}
		index, ok := userIndex[ticket.UserID]
		if ok {
			decision.CustomerID = plan.Users[index].CustomerID
		}
		for _, message := range ticket.Messages {
			if utf8.RuneCountInString(TicketMessageBody(message)) > maxMessageBody {
				decision.Warnings = append(decision.Warnings, CodeMessageTruncated)
				break
			}
		}
		switch {
		case !ok || !plan.Users[index].Importable():
			decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		case len(ticket.Messages) == 0:
			decision.Action, decision.Codes = ActionSkip, []string{CodeEmptyTicket}
		case target.LegacyTickets[ticket.ID]:
			decision.Action = ActionUnchanged
		default:
			decision.Action = ActionCreate
		}
		plan.Tickets = append(plan.Tickets, decision)
	}
}

// ticketStatus maps Bedolaga's ticket states onto Omniflow's: an answered
// ticket waits on the customer, which Omniflow calls pending.
func ticketStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "closed":
		return "closed"
	case "answered":
		return "pending"
	case "resolved":
		return "resolved"
	default:
		return "open"
	}
}

func ticketPriority(priority string) string {
	switch value := strings.ToLower(strings.TrimSpace(priority)); value {
	case "low", "normal", "high", "urgent":
		return value
	default:
		return "normal"
	}
}

// TicketMessageBody is the text a carried message shows: its words, and a
// line saying a file was attached, since the file stays in the old chat.
func TicketMessageBody(message TicketMessage) string {
	parts := make([]string, 0, 3)
	if text := strings.TrimSpace(message.Text); text != "" {
		parts = append(parts, text)
	}
	if message.HasMedia {
		marker := "📎 " + mediaName(message.MediaType)
		if caption := strings.TrimSpace(message.Caption); caption != "" && caption != strings.TrimSpace(message.Text) {
			marker += ": " + caption
		}
		parts = append(parts, marker)
	}
	body := strings.Join(parts, "\n\n")
	if body == "" {
		body = "—"
	}
	return truncateRunes(body, maxMessageBody)
}

func mediaName(kind string) string {
	switch strings.ToLower(kind) {
	case "photo":
		return "фото"
	case "video":
		return "видео"
	case "voice", "audio":
		return "аудио"
	case "document":
		return "файл"
	default:
		return "вложение"
	}
}

func decideConsents(plan *Plan, source Source, target Target, userIndex map[int64]int) {
	consents := append([]Consent(nil), source.Consents...)
	sort.Slice(consents, func(i, j int) bool { return consents[i].ID < consents[j].ID })
	for _, consent := range consents {
		decision := ConsentDecision{Source: consent, Purpose: consentPurpose(consent.Document)}
		index, ok := userIndex[consent.UserID]
		if ok {
			decision.CustomerID = plan.Users[index].CustomerID
		}
		switch {
		case !ok || !plan.Users[index].Importable():
			decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		case decision.Purpose == "":
			decision.Action, decision.Codes = ActionSkip, []string{CodeConsentDocument}
		case target.LegacyConsents[consent.ID]:
			decision.Action = ActionUnchanged
		default:
			decision.Action = ActionCreate
		}
		plan.Consents = append(plan.Consents, decision)
	}
}

// consentPurpose is the Omniflow purpose a Bedolaga document was accepted
// for: the privacy policy is privacy, the public offer is the terms.
func consentPurpose(document string) string {
	switch strings.ToLower(strings.TrimSpace(document)) {
	case "privacy_policy", "privacy":
		return "privacy"
	case "public_offer", "offer", "terms", "rules", "service_rules":
		return "terms"
	default:
		return ""
	}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
