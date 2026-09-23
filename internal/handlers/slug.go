package handlers

import "strings"

// safeSlug : normalise un hostname en un slug utilisable comme nom de
// fichier. Utilisé pour générer le nom du vhost Nginx d'un domaine, et
// pour retrouver ce même fichier lors de la suppression.
//
// Exemple : "app.example.com" → "app.example.com"
//           "my_app.foo-bar.com" → "my-app.foo-bar.com"
func safeSlug(hostname string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(hostname) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}