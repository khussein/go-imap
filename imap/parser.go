package imap

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
)

func init() {
	log.SetFlags(log.Ltime | log.Lshortfile)
}

func recoverError(err *os.Error) {
	if e := recover(); e != nil {
		if osErr, ok := e.(os.Error); ok {
			*err = osErr
			return
		}
		panic(e)
	}
}

type Sexp interface{}
// One of:
//   string
//   []Sexp
//   nil
func nilOrString(s Sexp) *string {
	if s == nil {
		return nil
	}
	str := s.(string)
	return &str
}

type Parser struct {
	*bufio.Reader
}

func newParser(r io.Reader) *Parser {
	return &Parser{bufio.NewReader(r)}
}

func (p *Parser) expect(text string) os.Error {
	buf := make([]byte, len(text))

	_, err := io.ReadFull(p, buf)
	if err != nil {
		return err
	}

	if !bytes.Equal(buf, []byte(text)) {
		return fmt.Errorf("expected %q, got %q", text, buf)
	}

	return nil
}

func (p *Parser) expectEOL() os.Error {
	return p.expect("\r\n")
}

func (p *Parser) readToken() (token string, outErr os.Error) {
	defer recoverError(&outErr)

	buf := bytes.NewBuffer(make([]byte, 0, 16))
	for {
		c, err := p.ReadByte()
		check(err)
		switch c {
		case ' ':
			return buf.String(), nil
		case '\r':
			err := p.UnreadByte()
			check(err)
			return buf.String(), nil
		}
		buf.WriteByte(c)
	}

	panic("not reached")
}

func (p *Parser) readAtom() (outStr string, outErr os.Error) {
	/*
	ATOM-CHAR       = <any CHAR except atom-specials>

	atom-specials   = "(" / ")" / "{" / SP / CTL / list-wildcards /
	                  quoted-specials / resp-specials
	*/
	defer recoverError(&outErr)
	atom := bytes.NewBuffer(make([]byte, 0, 16))

	for {
		c, err := p.ReadByte()
		check(err)

		switch c {
		case '(', ')', '{', ' ',
			// XXX: CTL
			'%', '*', // list-wildcards
			'"': // quoted-specials
			// XXX: note that I dropped '\' from the quoted-specials,
			// because it conflicts with parsing flags.  Who knows.
			// XXX: resp-specials
			err = p.UnreadByte()
			check(err)
			return atom.String(), nil
		}

		atom.WriteByte(c)
	}

	panic("not reached")
}

func (p *Parser) readQuoted() (outStr string, outErr os.Error) {
	defer recoverError(&outErr)

	err := p.expect("\"")
	check(err)

	quoted := bytes.NewBuffer(make([]byte, 0, 16))

	for {
		c, err := p.ReadByte()
		check(err)
		switch c {
		case '\\':
			c, err = p.ReadByte()
			check(err)
			if c != '"' && c != '\\' {
				return "", fmt.Errorf("backslash-escaped %c", c)
			}
		case '"':
			return quoted.String(), nil
		}
		quoted.WriteByte(c)
	}

	panic("not reached")
}

func (p *Parser) readLiteral() (literal []byte, outErr os.Error) {
	/*
	literal         = "{" number "}" CRLF *CHAR8
	*/
	defer recoverError(&outErr)

	check(p.expect("{"))

	lengthBytes, err := p.ReadSlice('}')
	check(err)

	length, err := strconv.Atoi(string(lengthBytes[0 : len(lengthBytes)-1]))
	check(err)

	err = p.expect("\r\n")
	check(err)

	literal = make([]byte, length)
	_, err = io.ReadFull(p, literal)
	check(err)

	return
}

func (p *Parser) readBracketed() (text string, outErr os.Error) {
	defer recoverError(&outErr)

	check(p.expect("["))
	text, err := p.ReadString(']')
	check(err)

	return text, nil
}

func (p *Parser) readSexp() (sexp []Sexp, outErr os.Error) {
	defer recoverError(&outErr)

	err := p.expect("(")
	check(err)

	sexps := make([]Sexp, 0, 4)
	for {
		c, err := p.ReadByte()
		check(err)

		var exp Sexp
		switch c {
		case ')':
			return sexps, nil
		case '(':
			p.UnreadByte()
			exp, err = p.readSexp()
		case '"':
			p.UnreadByte()
			exp, err = p.readQuoted()
		case '{':
			p.UnreadByte()
			exp, err = p.readLiteral()
		default:
			// TODO: may need to distinguish atom from string in practice.
			p.UnreadByte()
			exp, err = p.readAtom()
			if exp == "NIL" {
				exp = nil
			}
		}
		check(err)

		sexps = append(sexps, exp)

		c, err = p.ReadByte()
		check(err)
		if c != ' ' {
			err = p.UnreadByte()
			check(err)
		}
	}

	panic("not reached")
}

func (p *Parser) readParenStringList() ([]string, os.Error) {
	sexp, err := p.readSexp()
	if err != nil {
		return nil, err
	}
	strs := make([]string, len(sexp))
	for i, s := range sexp {
		str, ok := s.(string)
		if !ok {
			return nil, fmt.Errorf("list element %d is %T, not string", i, s)
		}
		strs[i] = str
	}
	return strs, nil
}

func (p *Parser) readToEOL() (string, os.Error) {
	line, prefix, err := p.ReadLine()
	if err != nil {
		return "", err
	}
	if prefix {
		return "", os.NewError("got line prefix, buffer too small")
	}
	return string(line), nil
}