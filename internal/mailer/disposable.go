package mailer

import (
	"strings"
)

// disposableDomains are throwaway-mailbox services. The list is compiled in
// rather than downloaded, as Bedolaga does, so the check never makes a network
// request and never changes under a running installation; an operator adds
// local entries with APP_EMAIL_BLOCKED_DOMAINS.
var disposableDomains = map[string]bool{}

func init() {
	for _, domain := range strings.Fields(disposableList) {
		disposableDomains[domain] = true
	}
}

// DisposableChecker answers whether an address uses a throwaway domain.
type DisposableChecker struct {
	extra map[string]bool
}

// NewDisposableChecker adds the operator's own domains to the built-in list.
// The value may separate domains with commas, semicolons, or whitespace, and
// a leading "@" or "." is ignored.
func NewDisposableChecker(extra string) DisposableChecker {
	checker := DisposableChecker{extra: map[string]bool{}}
	for _, token := range strings.FieldsFunc(extra, func(character rune) bool {
		return character == ',' || character == ';' || character == ' ' || character == '\n' || character == '\t'
	}) {
		if domain := strings.Trim(strings.ToLower(strings.TrimSpace(token)), "@."); domain != "" {
			checker.extra[domain] = true
		}
	}
	return checker
}

// IsDisposable reports whether the address's domain, or any parent domain
// short of the bare top-level one, is on either list: "x.mail.tm" is caught
// by "mail.tm".
func (checker DisposableChecker) IsDisposable(address string) bool {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return false
	}
	domain := strings.Trim(strings.ToLower(strings.TrimSpace(address[at+1:])), ".")
	parts := strings.Split(domain, ".")
	for start := 0; start < len(parts)-1; start++ {
		candidate := strings.Join(parts[start:], ".")
		if disposableDomains[candidate] || checker.extra[candidate] {
			return true
		}
	}
	return false
}

// IsDisposable checks the built-in list only.
func IsDisposable(address string) bool { return DisposableChecker{}.IsDisposable(address) }

// disposableList is a curated subset of the community list at
// github.com/disposable/disposable-email-domains: the services that account
// for nearly all throwaway sign-ups in practice.
const disposableList = `
0-mail.com 0815.ru 10minutemail.com 10minutemail.net 10minutemail.co.uk 20minutemail.com
33mail.com 4warding.com 6paq.com 7tags.com 9ox.net a-bc.net anonbox.net anonymbox.com
binkmail.com bobmail.info bofthew.com boximail.com bugmenot.com burnermail.io byom.de
chammy.info cuvox.de dayrep.com deadaddress.com despam.it discard.email
discardmail.com discardmail.de dispostable.com dodgeit.com dodgit.com dropmail.me
e4ward.com einrot.com email-fake.com emailfake.com emailondeck.com emailsensei.com
emailtemporanea.net emailtemporar.ro emailtemporario.com.br emailwarden.com emailx.at.hm
emltmp.com ephemail.net etranquil.com fakeinbox.com fakemail.net fakemailgenerator.com
fastacura.com filzmail.com fleckens.hu getairmail.com getnada.com gishpuppy.com
grr.la guerrillamail.biz guerrillamail.com guerrillamail.de guerrillamail.info
guerrillamail.net guerrillamail.org guerrillamailblock.com gustr.com harakirimail.com
hidemail.de hmamail.com hulapla.de imgof.com imstations.com incognitomail.com
incognitomail.org inboxalias.com inboxbear.com inboxkitten.com jetable.com jetable.org
jourrapide.com kasmail.com kaspop.com killmail.com klzlk.com kurzepost.de laafd.com
lackmail.net letthemeatspam.com lhsdv.com lookugly.com lroid.com mail-temp.com mail.tm
mail7.io maildrop.cc mailcatch.com maildx.com mailexpire.com mailforspam.com mailfreeonline.com
mailinator.com mailinator.net mailinator2.com mailmetrash.com mailmoat.com mailnesia.com
mailnull.com mailsac.com mailshell.com mailtemp.info mailtothis.com meltmail.com
mintemail.com moakt.com mohmal.com mt2015.com mytemp.email mytrashmail.com nada.email
nomail.xl.cx nospam.ze.tc nowmymail.com objectmail.com obobbo.com
oneoffemail.com onewaymail.com owlpic.com pookmail.com proxymail.eu punkass.com
putthisinyourspamdatabase.com quickinbox.com rcpt.at recode.me rhyta.com rmqkr.net
safetymail.info sharklasers.com shieldemail.com shitmail.me smellfear.com snakemail.com
sneakemail.com sofimail.com spam4.me spamavert.com spambog.com spambox.us spamcero.com
spamday.com spamex.com spamfree24.org spamgourmet.com spamhole.com spamify.com
spaml.com spammotel.com spamobox.com spamspot.com spamthis.co.uk superrito.com
suremail.info teleworm.us temp-mail.io temp-mail.org temp-mail.ru tempail.com
tempemail.net tempinbox.com tempmail.dev tempmail.net tempmail.plus tempmailo.com
tempmailaddress.com tempr.email tempsky.com thankyou2010.com thisisnotmyrealemail.com
throwam.com throwawayemailaddress.com throwawaymail.com tmail.ws tmailinator.com
tmpmail.net tmpmail.org trash-mail.com trashmail.at trashmail.com trashmail.de
trashmail.io trashmail.me trashmail.net trashymail.com trbvm.com tyldd.com
uggsrock.com wegwerfemail.de wegwerfmail.de wegwerfmail.net wh4f.org yepmail.net
yopmail.com yopmail.fr yopmail.net zetmail.com zoemail.org 1secmail.com 1secmail.net
1secmail.org emailnax.com emlhub.com emlpro.com fexbox.org fextemp.com
minuteinbox.com spymail.one tempmail.lol tempm.com vomoto.com xojxe.com zslsz.com
`
