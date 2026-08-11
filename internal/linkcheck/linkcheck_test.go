package linkcheck

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func codes(findings []Finding) []string {
	seen := make([]string, 0, len(findings))
	for _, finding := range findings {
		seen = append(seen, finding.Code)
	}
	return seen
}

func checker() *Checker {
	return New(Options{TrustedHosts: []string{"panel.example.com", "example.com"}})
}

// The structural checks cost nothing and reveal nothing, so they always run.
func TestStructuralChecksNeedNoConfiguration(t *testing.T) {
	plain := New(Options{})
	if plain.ReputationConfigured() {
		t.Fatal("a checker invented a reputation tool")
	}
	findings := plain.Inspect(context.Background(), []Link{
		{URL: "http://admin:hunter2@192.168.1.5:8080/login"},
	}, nil)
	for _, expected := range []string{
		FindingCredentialInURL, FindingIPLiteral, FindingUnusualPort, FindingPlaintext,
	} {
		if !slices.Contains(codes(findings), expected) {
			t.Fatalf("missing %s: %v", expected, codes(findings))
		}
	}
}

// A punycode host may display as a different domain than the one it resolves
// to, which is the entire attack.
func TestPunycodeIsFlagged(t *testing.T) {
	findings := checker().Inspect(context.Background(),
		[]Link{{URL: "https://xn--telegram-1fc.com/login"}}, nil)
	if !slices.Contains(codes(findings), FindingPunycode) {
		t.Fatalf("a punycode host passed: %v", codes(findings))
	}
	for _, finding := range findings {
		if finding.Code == FindingPunycode && finding.Severity != SeverityDanger {
			t.Fatalf("punycode was not treated as dangerous: %+v", finding)
		}
	}
}

// A domain built from digit substitutions reads as another one at a glance.
func TestLookalikeDomainsAreFlaggedAndRealOnesAreNot(t *testing.T) {
	suspicious := checker().Inspect(context.Background(),
		[]Link{{URL: "https://te1egram-support.com/verify"}}, nil)
	if !slices.Contains(codes(suspicious), FindingLookalike) {
		t.Fatalf("a lookalike domain passed: %v", codes(suspicious))
	}

	// The installation's own host produces nothing at all.
	own := checker().Inspect(context.Background(),
		[]Link{{URL: "https://panel.example.com/admin/support"}}, nil)
	if len(own) != 0 {
		t.Fatalf("the installation's own link warned: %+v", own)
	}
}

// An anchor reading "your invoice" pointing somewhere else is the oldest trick
// here, and it is invisible without both halves.
func TestAnchorTextIsComparedWithItsTarget(t *testing.T) {
	findings := checker().Inspect(context.Background(), []Link{{
		URL: "https://collect-now.example.net/pay", Text: "panel.example.com/billing",
	}}, nil)
	if !slices.Contains(codes(findings), FindingAnchorMismatch) {
		t.Fatalf("a mismatched anchor passed: %v", codes(findings))
	}

	// Matching text, and a subdomain of what the text claims, are both fine.
	for _, link := range []Link{
		{URL: "https://docs.example.org/refunds", Text: "docs.example.org"},
		{URL: "https://help.docs.example.org/x", Text: "docs.example.org"},
		{URL: "https://docs.example.org/refunds", Text: "our refund policy"},
	} {
		if findings := checker().Inspect(context.Background(), []Link{link}, nil); slices.Contains(
			codes(findings), FindingAnchorMismatch,
		) {
			t.Fatalf("%q warned against %q: %v", link.Text, link.URL, codes(findings))
		}
	}
}

// "invoice.pdf.exe" is how an executable is dressed as a document;
// "report.2026.pdf" is ordinary.
func TestAttachmentNamesAreReadCarefully(t *testing.T) {
	findings := checker().Inspect(context.Background(), nil, []Attachment{
		{Filename: "invoice.pdf.exe", DeclaredType: "application/pdf"},
	})
	for _, expected := range []string{FindingExecutable, FindingDoubleExtension} {
		if !slices.Contains(codes(findings), expected) {
			t.Fatalf("missing %s: %v", expected, codes(findings))
		}
	}

	ordinary := checker().Inspect(context.Background(), nil, []Attachment{
		{Filename: "report.2026.pdf", DeclaredType: "application/pdf"},
	})
	if len(ordinary) != 0 {
		t.Fatalf("an ordinary filename warned: %+v", ordinary)
	}
}

// A disagreement between the name and the declared type is visible without
// opening the file, and opening it is a parser this package will not have.
func TestADeclaredTypeThatDisagreesWithTheNameIsFlagged(t *testing.T) {
	findings := checker().Inspect(context.Background(), nil, []Attachment{
		{Filename: "photo.png", DeclaredType: "application/x-msdownload"},
	})
	if !slices.Contains(codes(findings), FindingTypeMismatch) {
		t.Fatalf("a mismatched type passed: %v", codes(findings))
	}

	agreeing := checker().Inspect(context.Background(), nil, []Attachment{
		{Filename: "photo.png", DeclaredType: "image/png", SizeBytes: 4096},
	})
	if len(agreeing) != 0 {
		t.Fatalf("an ordinary attachment warned: %+v", agreeing)
	}
}

// stubReputation records what it was asked, so a test can prove a link never
// left.
type stubReputation struct {
	asked    []string
	findings []Finding
	err      error
}

func (stub *stubReputation) Name() string { return "stub-reputation" }

func (stub *stubReputation) Check(_ context.Context, target string) ([]Finding, error) {
	stub.asked = append(stub.asked, target)
	return stub.findings, stub.err
}

// An integration that quietly appears is a data-sharing decision somebody did
// not make.
func TestNothingLeavesUntilAnOwnerConfiguresATool(t *testing.T) {
	stub := &stubReputation{}
	unconfigured := New(Options{})
	unconfigured.Inspect(context.Background(), []Link{{URL: "https://ordinary.example.net/x"}}, nil)
	if len(stub.asked) != 0 {
		t.Fatal("a link left an installation with no configured tool")
	}

	configured := New(Options{Reputation: stub, TrustedHosts: []string{"example.com"}})
	if !configured.ReputationConfigured() {
		t.Fatal("a configured tool was not reported")
	}
	configured.Inspect(context.Background(), []Link{{URL: "https://ordinary.example.net/x"}}, nil)
	if len(stub.asked) != 1 || stub.asked[0] != "https://ordinary.example.net/x" {
		t.Fatalf("the configured tool was not consulted: %v", stub.asked)
	}
}

// A link already known to be malformed does not need a third party's opinion,
// and not sending it is one fewer disclosure.
func TestAlreadySuspiciousLinksAreNotSentOut(t *testing.T) {
	stub := &stubReputation{}
	configured := New(Options{Reputation: stub})
	findings := configured.Inspect(context.Background(),
		[]Link{{URL: "https://xn--telegram-1fc.com/login"}}, nil)

	if len(stub.asked) != 0 {
		t.Fatalf("a structurally suspicious link was disclosed anyway: %v", stub.asked)
	}
	if !slices.Contains(codes(findings), FindingPunycode) {
		t.Fatalf("the structural finding was lost: %v", codes(findings))
	}
}

// Silence reads as "clean", so a tool that fails says so.
func TestAFailedCheckIsReportedRatherThanIgnored(t *testing.T) {
	stub := &stubReputation{err: errors.New("upstream is down")}
	configured := New(Options{Reputation: stub})
	findings := configured.Inspect(context.Background(),
		[]Link{{URL: "https://ordinary.example.net/x"}}, nil)

	if !slices.Contains(codes(findings), FindingCheckerFailed) {
		t.Fatalf("a failed check was silent: %v", codes(findings))
	}
	for _, finding := range findings {
		if finding.Source != "stub-reputation" {
			t.Fatalf("the finding does not say whose verdict it is: %+v", finding)
		}
	}
}

// An operator seeing a reputation verdict should know whose verdict it is.
func TestReputationFindingsAreAttributed(t *testing.T) {
	stub := &stubReputation{findings: []Finding{{
		Severity: SeverityDanger, Detail: "Known phishing host.",
	}}}
	configured := New(Options{Reputation: stub})
	findings := configured.Inspect(context.Background(),
		[]Link{{URL: "https://ordinary.example.net/x"}}, nil)

	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %+v", findings)
	}
	if findings[0].Source != "stub-reputation" || findings[0].Code != FindingReputation {
		t.Fatalf("the verdict was not attributed or coded: %+v", findings[0])
	}
	if findings[0].Subject != "https://ordinary.example.net/x" {
		t.Fatalf("the verdict does not name its link: %+v", findings[0])
	}
}

// An operator triaging a queue reads the worst thing first.
func TestFindingsAreOrderedBySeverity(t *testing.T) {
	findings := checker().Inspect(context.Background(),
		[]Link{{URL: "http://xn--paypa1-6ve.com:8443/login"}}, nil)
	if len(findings) < 2 {
		t.Fatalf("expected several findings, got %+v", findings)
	}
	if findings[0].Severity != SeverityDanger {
		t.Fatalf("the worst finding is not first: %+v", findings)
	}
}

// Each caller writing its own regular expression is each caller getting it
// slightly wrong.
func TestLinksAreExtractedFromPlainText(t *testing.T) {
	links := LinksIn("See https://example.com/a, and https://example.com/b. " +
		"Also https://example.com/a again.")
	if len(links) != 2 {
		t.Fatalf("expected two distinct links, got %+v", links)
	}
	for _, link := range links {
		if strings.HasSuffix(link.URL, ",") || strings.HasSuffix(link.URL, ".") {
			t.Fatalf("trailing punctuation was kept: %q", link.URL)
		}
	}
}
