package db0301

type Parser struct {
	buf string
	pos int
}

func NewParser(s string) Parser {
	return Parser{buf: s, pos: 0}
}

func isSpace(ch byte) bool {
	switch ch {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}
func isAlpha(ch byte) bool {
	return 'a' <= (ch|32) && (ch|32) <= 'z'
}
func isDigit(ch byte) bool {
	return '0' <= ch && ch <= '9'
}
func isNameStart(ch byte) bool {
	return isAlpha(ch) || ch == '_'
}
func isNameContinue(ch byte) bool {
	return isAlpha(ch) || isDigit(ch) || ch == '_'
}
func isSeparator(ch byte) bool {
	return ch < 128 && !isNameContinue(ch)
}

func (p *Parser) skipSpaces() {
	for p.pos < len(p.buf) && isSpace(p.buf[p.pos]) {
		p.pos += 1
	}
}

func (p *Parser) tryKeyword(kw string) bool {
	p.skipSpaces()
	pos := p.pos
	for i := 0; i < len(kw); i++ {
		if pos+i >= len(p.buf) {
			return false
		}
		if (p.buf[pos+i] | 32) != (kw[i] | 32) {
			return false
		}
	}
	end := pos + len(kw)
	if end < len(p.buf) && !isSeparator(p.buf[end]) {
		return false
	}
	p.pos = end
	return true
}

func (p *Parser) tryName() (string, bool) {
	p.skipSpaces()
	if p.pos >= len(p.buf) || !isNameStart(p.buf[p.pos]) {
		return "", false
	}
	start := p.pos
	for p.pos < len(p.buf) && isNameContinue(p.buf[p.pos]) {
		p.pos++
	}
	return p.buf[start:p.pos], true
}

func (p *Parser) isEnd() bool {
	p.skipSpaces()
	return p.pos >= len(p.buf)
}

// QzBQWVJJOUhU https://trialofcode.org/
