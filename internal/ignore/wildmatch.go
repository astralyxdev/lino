package ignore

// Port of git's wildmatch.c with WM_PATHNAME always set: '*' and '?' never
// match '/', "**" matches across directories when it forms a whole segment.

const (
	wmMatch = iota
	wmNoMatch
	wmAbortAll
	wmAbortToStarStar
)

func wildmatch(pattern, text string) bool {
	return dowild(pattern, text) == wmMatch
}

func at(s string, i int) byte {
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return 0
}

func isGlobSpecial(c byte) bool {
	return c == '*' || c == '?' || c == '[' || c == '\\'
}

func dowild(pat, text string) int {
	p, t := 0, 0
	for ; p < len(pat); p, t = p+1, t+1 {
		pCh := pat[p]
		tCh := at(text, t)
		if tCh == 0 && pCh != '*' {
			return wmAbortAll
		}
		switch pCh {
		case '\\':
			p++
			pCh = at(pat, p)
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		default:
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		case '?':
			if tCh == '/' {
				return wmNoMatch
			}
			continue
		case '*':
			var matchSlash bool
			p++
			if at(pat, p) == '*' {
				prev := p - 2
				for p++; at(pat, p) == '*'; p++ {
				}
				if (prev < 0 || pat[prev] == '/') &&
					(at(pat, p) == 0 || at(pat, p) == '/' || (at(pat, p) == '\\' && at(pat, p+1) == '/')) {
					if at(pat, p) == '/' && dowild(pat[p+1:], text[t:]) == wmMatch {
						return wmMatch
					}
					matchSlash = true
				}
			}
			if p >= len(pat) {
				if !matchSlash && indexByte(text[t:], '/') >= 0 {
					return wmNoMatch
				}
				return wmMatch
			}
			if !matchSlash && pat[p] == '/' {
				i := indexByte(text[t:], '/')
				if i < 0 {
					return wmNoMatch
				}
				t += i
				continue
			}
			for tCh != 0 {
				if !isGlobSpecial(pat[p]) {
					pc := pat[p]
					for tCh = at(text, t); tCh != 0 && (matchSlash || tCh != '/'); tCh = at(text, t) {
						if tCh == pc {
							break
						}
						t++
					}
					if tCh != pc {
						return wmNoMatch
					}
				}
				if m := dowild(pat[p:], text[t:]); m != wmNoMatch {
					if !matchSlash || m != wmAbortToStarStar {
						return m
					}
				} else if !matchSlash && tCh == '/' {
					return wmAbortToStarStar
				}
				t++
				tCh = at(text, t)
			}
			return wmAbortAll
		case '[':
			p++
			pCh = at(pat, p)
			if pCh == '^' {
				pCh = '!'
			}
			negated := pCh == '!'
			if negated {
				p++
				pCh = at(pat, p)
			}
			var prevCh byte
			matched := false
			for first := true; ; first = false {
				if !first {
					prevCh = pCh
					p++
					pCh = at(pat, p)
					if pCh == ']' {
						break
					}
				}
				if pCh == 0 {
					return wmAbortAll
				}
				switch {
				case pCh == '\\':
					p++
					pCh = at(pat, p)
					if pCh == 0 {
						return wmAbortAll
					}
					if tCh == pCh {
						matched = true
					}
				case pCh == '-' && prevCh != 0 && at(pat, p+1) != 0 && at(pat, p+1) != ']':
					p++
					pCh = at(pat, p)
					if pCh == '\\' {
						p++
						pCh = at(pat, p)
						if pCh == 0 {
							return wmAbortAll
						}
					}
					if tCh <= pCh && tCh >= prevCh {
						matched = true
					}
					pCh = 0
				case pCh == '[' && at(pat, p+1) == ':':
					p += 2
					s := p
					for pCh = at(pat, p); pCh != 0 && pCh != ']'; pCh = at(pat, p) {
						p++
					}
					if pCh == 0 {
						return wmAbortAll
					}
					if p-s-1 < 0 || pat[p-1] != ':' {
						p = s - 2
						pCh = '['
						if tCh == pCh {
							matched = true
						}
						continue
					}
					ok, known := charClass(pat[s:p-1], tCh)
					if !known {
						return wmAbortAll
					}
					if ok {
						matched = true
					}
					pCh = 0
				default:
					if tCh == pCh {
						matched = true
					}
				}
			}
			if matched == negated || tCh == '/' {
				return wmNoMatch
			}
			continue
		}
	}
	if t < len(text) {
		return wmNoMatch
	}
	return wmMatch
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func charClass(name string, c byte) (match, known bool) {
	isUpper := c >= 'A' && c <= 'Z'
	isLower := c >= 'a' && c <= 'z'
	isDigit := c >= '0' && c <= '9'
	isAlpha := isUpper || isLower
	isSpace := c == ' ' || (c >= '\t' && c <= '\r')
	isPrint := c >= 0x20 && c < 0x7f
	switch name {
	case "alnum":
		return isAlpha || isDigit, true
	case "alpha":
		return isAlpha, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 0x20 || c == 0x7f, true
	case "digit":
		return isDigit, true
	case "graph":
		return isPrint && c != ' ', true
	case "lower":
		return isLower, true
	case "print":
		return isPrint, true
	case "punct":
		return isPrint && c != ' ' && !isAlpha && !isDigit, true
	case "space":
		return isSpace, true
	case "upper":
		return isUpper, true
	case "xdigit":
		return isDigit || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'), true
	}
	return false, false
}

// Glob reports whether name matches pattern with git wildmatch rules:
// '*' and '?' stop at '/', "**" spans directories.
func Glob(pattern, name string) bool {
	return wildmatch(pattern, name)
}
