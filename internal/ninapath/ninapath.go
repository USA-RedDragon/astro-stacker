// Package ninapath names files and folders the way N.I.N.A. does, so a key
// built from a target or filter name matches the folder NINA saved it in.
package ninapath

import "strings"

// Folder is the folder NINA saves a target's images in, from
// CoreUtil.ReplaceAllInvalidFilenameChars: path separators become "-", the
// other characters Windows refuses in a file name become "_", and the name
// is trimmed. "." and ".." become underscores so a key built from one stays
// in its prefix.
func Folder(name string) string {
	s := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), `\`, "-"), "/", "-")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`"<>|:*?`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return strings.Repeat("_", len(s))
	}
	return s
}
