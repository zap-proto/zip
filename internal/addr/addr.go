// Package addr spells an address, once, for everything that reads one.
//
// It exists because the rule was written twice — the router composed a group's
// prefix with a leaf, and cmd/zipdoc composed the same two things to find the
// prose for that route — and two copies of one rule drift. A pattern declared
// under one spelling and filed under the other publishes an operation with no
// description, which is silent: the document is complete, the sentence is
// simply missing.
package addr

// Norm is a path in its ONE canonical spelling.
//
// A pattern never ends in "/" — the root aside — because the router serves "/x"
// and "/x/" alike, so two spellings are two keys for one address: the document
// publishes one, the op is filed under the other, and a lookup that composed the
// pattern itself misses.
func Norm(path string) string {
	if path == "" {
		return "/"
	}
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}

// Join composes a group's prefix with a leaf, the way the router does.
//
// An EMPTY LEAF is the prefix itself: declaring "" on a group names the group's
// own address, which is how a collection root is spelled. It used to name the
// prefix with a trailing slash, so a group could not name its own root and a
// service reached for an absolute path on the parent instead — which put the
// route outside the middleware of the group it belonged to.
func Join(prefix, path string) string {
	if prefix == "" {
		return Norm(path)
	}
	if path == "" || path == "/" {
		return Norm(trimRight(prefix))
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return Norm(trimRight(prefix) + path)
}

func trimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
