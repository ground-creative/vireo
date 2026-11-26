package protocol

// canonical.go implements RFC 8785 (JSON Canonicalization Scheme) for the
// manifest digest defined by MAN-40.
//
// The digest is only useful if a client written in another language computes
// the same string from the same document. That requires canonicalisation to be
// specified rather than incidental, and the two obvious shortcuts are both
// wrong:
//
//   - encoding/json sorts map members by Unicode code point; RFC 8785 sorts by
//     UTF-16 code unit. The orderings differ for characters outside the BMP.
//   - encoding/json escapes <, > and & unless told not to, and formats floats
//     with Go's rules rather than ECMAScript's. A digest taken over either is
//     reproducible only by Go.
//
// So the canonical form is built here, byte by byte, and the numbers follow
// ECMAScript Number::toString exactly as RFC 8785 §3.2.2.3 requires.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// CanonicalJSON returns the RFC 8785 serialisation of a decoded JSON value.
// The value must be what encoding/json produces with UseNumber, so that number
// literals survive unchanged and can be formatted deliberately.
func CanonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeString(buf, v)
	case json.Number:
		number, err := esNumber(string(v))
		if err != nil {
			return err
		}
		buf.WriteString(number)
	case float64:
		number, err := esNumber(strconv.FormatFloat(v, 'g', -1, 64))
		if err != nil {
			return err
		}
		buf.WriteString(number)
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// RFC 8785 §3.2.3: sort by UTF-16 code units, not code points.
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: unsupported value %T", value)
	}
	return nil
}

// writeString emits a JSON string with only the escapes RFC 8785 permits:
// quote, backslash and the C0 controls. Everything else, including non-ASCII,
// is UTF-8 as-is. This is the difference that makes a description in Japanese
// digest the same in every implementation.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
				continue
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// utf16Less orders two strings by their UTF-16 code units, which is what
// ECMAScript's default sort does and what RFC 8785 requires.
func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// esNumber converts a JSON number literal to ECMAScript Number::toString form.
//
// The rules, from RFC 8785 §3.2.2.3 (which delegates to ECMAScript):
//
//	let n, k, s be the shortest digits with value s × 10^(n−k)
//	if k ≤ n ≤ 21      -> digits then (n−k) zeros
//	if 0 < n ≤ 21      -> digits with a point after n
//	if −6 < n ≤ 0      -> "0." then (−n) zeros then digits
//	otherwise          -> one digit, optional ".digits", "e", sign, exponent
//
// The sign is always written for a positive exponent and never for a negative
// one beyond "-"; `-0` serialises as `0`.
func esNumber(literal string) (string, error) {
	literal = strings.TrimSpace(literal)
	if literal == "" {
		return "", fmt.Errorf("canonical: empty number")
	}

	// An integer literal is already canonical once its sign and leading zeros
	// are normalised; no float machinery can represent 20-digit ids safely.
	if !strings.ContainsAny(literal, ".eE") {
		neg := strings.HasPrefix(literal, "-")
		digits := strings.TrimLeft(strings.TrimPrefix(literal, "-"), "0")
		if digits == "" {
			return "0", nil // covers "-0" and all-zero literals
		}
		if neg {
			return "-" + digits, nil
		}
		return digits, nil
	}

	f, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return "", fmt.Errorf("canonical: %q is not a number: %w", literal, err)
	}
	if f != f {
		return "", fmt.Errorf("canonical: NaN is not representable in JSON")
	}
	if f == 0 {
		return "0", nil
	}

	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}

	// Shortest round-trip digits and decimal exponent, in the form d.dddde±XX.
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, expPart, ok := strings.Cut(sci, "e")
	if !ok {
		return "", fmt.Errorf("canonical: unexpected float form %q", sci)
	}
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return "", fmt.Errorf("canonical: bad exponent in %q: %w", sci, err)
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return "0", nil
	}
	k := len(digits)
	n := k + exp

	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}
	mant := digits
	if k > 1 {
		mant = digits[:1] + "." + digits[1:]
	}
	e := n - 1
	expSign := "+"
	if e < 0 {
		expSign = "-"
		e = -e
	}
	return sign + mant + "e" + expSign + strconv.Itoa(e), nil
}

// canonicalDocument canonicalises a decoded JSON document. Numbers arrive as
// json.Number because every caller decodes with UseNumber.
func canonicalDocument(doc map[string]any) ([]byte, error) {
	return CanonicalJSON(doc)
}

// decodeCanonical parses raw into a generic value with number literals intact.
func decodeCanonical(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
