package json

import (
	"bytes"
	"io"
)

const (
	ObjectStart = '{' // {
	ObjectEnd   = '}' // }
	String      = '"' // "
	Colon       = ':' // :
	Comma       = ',' // ,
	ArrayStart  = '[' // [
	ArrayEnd    = ']' // ]
	True        = 't' // t
	False       = 'f' // f
	Null        = 'n' // n
)

// NewScanner returns a new Scanner for the io.Reader r.
// A Scanner reads from the supplied io.Reader and produces via Next a stream
// of tokens, expressed as []byte slices.
func NewScanner(r io.Reader) *Scanner {
	return &Scanner{
		br: byteReader{
			r: r,
		},
	}
}

// NewScannerBuffer returns new scanner with specified buffer to use.
func NewScannerBuffer(r io.Reader, buf []byte) *Scanner {
	return &Scanner{
		br: byteReader{
			r:    r,
			data: buf[:0],
		},
	}
}

// Scanner implements a JSON scanner as defined in RFC 7159.
type Scanner struct {
	br   byteReader
	pos  int
	peek []byte
}

var whitespace = [256]bool{
	' ':  true,
	'\r': true,
	'\n': true,
	'\t': true,
}

func (s *Scanner) InputOffset() int {
	return s.br.inputOffset + s.pos
}

// Buffered returns a reader for the data remaining in the Scanner's buffer.
// The buffer starts right after the last token read by Next().
// Current Peek'ed token (if any) is inserted in the front.
// The reader is valid until the next call to Next, NextToken.
func (s *Scanner) Buffered() io.Reader {
	if s.peek != nil {
		return bytes.NewReader(s.br.window(0))
	}
	return bytes.NewReader(s.br.window(s.pos))
}

// Buffer returns raw underlying buffer.
// The returned buffer may differ from the original buffer, due to grow while decoding.
// Buffer() may be useful for buffer pools, to put after use.
func (s *Scanner) Buffer() []byte {
	return s.br.data
}

// Source returns underlying reader.
// Note that some bytes from Source may be already buffered.
func (s *Scanner) Source() io.Reader {
	return s.br.r
}

// Reader returns io.Reader for the unconsumed part of source stream.
// It combines the buffered part (see description for Buffered)
// with the source reader.
func (s *Scanner) Reader() io.Reader {
	return io.MultiReader(
		s.Buffered(),
		s.Source(),
	)
}

// Peek returns next token without consuming it.
func (s *Scanner) Peek() []byte {
	if s.peek == nil {
		s.peek = s.Next()
	}
	return s.peek
}

// Next returns a []byte referencing the the next lexical token in the stream.
// The []byte is valid until Next is called again.
// If the stream is at its end, or an error has occured, Next returns a zero
// length []byte slice.
//
// A valid token begins with one of the following:
//
//	{ Object start
//	[ Array start
//	} Object end
//	] Array End
//	, Literal comma
//	: Literal colon
//	t JSON true
//	f JSON false
//	n JSON null
//	" A string, possibly containing backslash escaped entites.
//	-, 0-9 A number
func (s *Scanner) Next() []byte {
	if s.peek != nil {
		b := s.peek
		s.peek = nil
		return b
	}

	s.br.release(s.pos)
	w := s.br.window(0)
loop:
	for pos, c := range w {
		// strip any leading whitespace.
		if whitespace[c] {
			continue
		}

		// simple case
		switch c {
		case ObjectStart, ObjectEnd, Colon, Comma, ArrayStart, ArrayEnd:
			s.pos = pos + 1
			return w[pos:s.pos]
		}

		s.br.release(pos)
		switch c {
		case True:
			s.pos = validateToken(&s.br, "true")
		case False:
			s.pos = validateToken(&s.br, "false")
		case Null:
			s.pos = validateToken(&s.br, "null")
		case String:
			if s.parseString() < 2 {
				return nil
			}
		default:
			// ensure the number is correct.
			s.pos = s.parseNumber(c)
		}
		return s.br.window(0)[:s.pos]
	}

	// it's all whitespace, ignore it
	s.br.release(len(w))

	// refill buffer
	if s.br.extend() == 0 {
		// eof
		return nil
	}
	w = s.br.window(0)
	goto loop
}

func validateToken(br *byteReader, expected string) int {
	for {
		w := br.window(0)
		n := len(expected)
		if len(w) >= n {
			if string(w[:n]) != expected {
				// doesn't match
				return 0
			}
			return n
		}
		// not enough data is left, we need to extend
		if br.extend() == 0 {
			// eof
			return 0
		}
	}
}

// parseString returns the length of the string token
// located at the start of the window or 0 if there is no closing
// " before the end of the byteReader.
func (s *Scanner) parseString() int {
	escaped := false
	w := s.br.window(1)
	pos := 0
	for {
		for _, c := range w {
			pos++
			switch {
			case escaped:
				escaped = false
			case c == '"':
				// finished
				s.pos = pos + 1
				return s.pos
			case c == '\\':
				escaped = true
			}
		}
		// need more data from the pipe
		if s.br.extend() == 0 {
			// EOF.
			return 0
		}
		w = s.br.window(pos + 1)
	}
}

func (s *Scanner) parseNumber(c byte) int {
	const (
		begin = iota
		leadingzero
		anydigit1
		decimal
		anydigit2
		exponent
		expsign
		anydigit3
	)

	pos := 0
	w := s.br.window(0)
	// int vs uint8 costs 10% on canada.json
	var state uint8 = begin

	// handle the case that the first character is a hyphen
	if c == '-' {
		pos++
		w = s.br.window(1)
	}

	for {
		for _, elem := range w {
			switch state {
			case begin:
				if elem >= '1' && elem <= '9' {
					state = anydigit1
				} else if elem == '0' {
					state = leadingzero
				} else {
					// error
					return 0
				}
			case anydigit1:
				if elem >= '0' && elem <= '9' {
					// stay in this state
					break
				}
				fallthrough
			case leadingzero:
				if elem == '.' {
					state = decimal
					break
				}
				if elem == 'e' || elem == 'E' {
					state = exponent
					break
				}
				return pos // finished.
			case decimal:
				if elem >= '0' && elem <= '9' {
					state = anydigit2
				} else {
					// error
					return 0
				}
			case anydigit2:
				if elem >= '0' && elem <= '9' {
					break
				}
				if elem == 'e' || elem == 'E' {
					state = exponent
					break
				}
				return pos // finished.
			case exponent:
				if elem == '+' || elem == '-' {
					state = expsign
					break
				}
				fallthrough
			case expsign:
				if elem >= '0' && elem <= '9' {
					state = anydigit3
					break
				}
				// error
				return 0
			case anydigit3:
				if elem < '0' || elem > '9' {
					return pos
				}
			}
			pos++
		}

		// need more data from the pipe
		if s.br.extend() == 0 {
			// end of the item. However, not necessarily an error. Make
			// sure we are in a state that allows ending the number.
			switch state {
			case leadingzero, anydigit1, anydigit2, anydigit3:
				return pos
			default:
				// error otherwise, the number isn't complete.
				return 0
			}
		}
		w = s.br.window(pos)
	}
}

// Error returns the first error encountered.
// When underlying reader is exhausted, Error returns io.EOF.
func (s *Scanner) Error() error { return s.br.err }
