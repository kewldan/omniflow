package accountsupport

import (
	"mime"
	"path"
	"strings"
)

// AcceptedFile is one upload that has passed every rule, in the exact shape the
// attachment row is written from. Validation returns it rather than mutating the
// caller's values so there is one place that decides what a file "is", and the
// database write has nothing left to interpret.
type AcceptedFile struct {
	// Kind is the `support_attachments.kind` vocabulary: 'photo' or 'document'.
	// It is derived from the media type rather than supplied, because the two
	// must not be able to disagree.
	Kind string
	// MediaType is the normalised type, with parameters such as `charset`
	// removed. The parameters are dropped rather than stored because they are
	// what an allowlist comparison would otherwise have to parse on every read.
	MediaType string
	// FileName is sanitised for display and for a Content-Disposition header.
	FileName  string
	SizeBytes int64
}

// MediaTypeAllowed reports whether a normalised media type is on the allowlist.
//
// The comparison is case-insensitive because a browser is free to send
// `IMAGE/PNG`, and refusing that would be a rule about capitalisation rather
// than about content.
func (limits Limits) MediaTypeAllowed(mediaType string) bool {
	normalised := strings.ToLower(strings.TrimSpace(mediaType))
	for _, allowed := range limits.AllowedMediaTypes {
		if strings.ToLower(strings.TrimSpace(allowed)) == normalised {
			return true
		}
	}
	return false
}

// Accept applies the size and media-type rules to one upload.
//
// The declared type is what is checked, and nothing sniffs the bytes to find a
// friendlier answer. Sniffing exists to guess at an unknown type, and a guess is
// precisely what an allowlist must not accept: a file whose type this
// installation has not said it wants is refused, not reinterpreted until it
// passes.
func (limits Limits) Accept(fileName, declaredType string, size int64) (AcceptedFile, error) {
	if size <= 0 {
		return AcceptedFile{}, invalid("the file is empty")
	}
	maximum := limits.MaxAttachmentBytes
	if maximum <= 0 || maximum > schemaMaxAttachmentBytes {
		maximum = schemaMaxAttachmentBytes
	}
	if size > maximum {
		return AcceptedFile{}, ErrAttachmentTooLarge
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(declaredType))
	if err != nil || mediaType == "" {
		// An upload that declares nothing, or declares something unparsable, is
		// refused here rather than defaulted to octet-stream. Defaulting would
		// turn "we do not know what this is" into a type the allowlist could be
		// asked about, which is the guess this rule exists to prevent.
		return AcceptedFile{}, ErrAttachmentMediaType
	}
	mediaType = strings.ToLower(mediaType)
	if !limits.MediaTypeAllowed(mediaType) {
		return AcceptedFile{}, ErrAttachmentMediaType
	}
	return AcceptedFile{
		Kind:      attachmentKind(mediaType),
		MediaType: mediaType,
		FileName:  SanitizeFileName(fileName),
		SizeBytes: size,
	}, nil
}

// attachmentKind maps a media type onto the two values the schema's `kind`
// column allows. Images are photos; everything else the allowlist permits is a
// document.
func attachmentKind(mediaType string) string {
	if strings.HasPrefix(mediaType, "image/") {
		return "photo"
	}
	return "document"
}

// SanitizeFileName reduces a supplied name to something safe to store and to
// echo back in a Content-Disposition header.
//
// A file name arrives from the customer's own machine and is displayed to an
// operator, so it is treated as hostile text rather than as a path: directory
// components are dropped so an upload cannot suggest a location, control
// characters and quotes are removed so a header cannot be split or reframed, and
// the result is bounded by the column's own limit. An empty result is not an
// error — the panel shows the media type instead, and refusing an upload over
// its name would be a rule about nothing.
func SanitizeFileName(value string) string {
	// Both separators are cut, not just the platform's: the name was produced by
	// whatever machine uploaded it, which is not this one.
	value = strings.ReplaceAll(value, "\\", "/")
	value = path.Base(strings.TrimSpace(value))
	if value == "." || value == "/" {
		return ""
	}
	cleaned := strings.Map(func(character rune) rune {
		switch {
		case character < 0x20 || character == 0x7f:
			return -1
		case character == '"' || character == '\\':
			return -1
		default:
			return character
		}
	}, value)
	return truncateRunes(strings.TrimSpace(cleaned), 200)
}
