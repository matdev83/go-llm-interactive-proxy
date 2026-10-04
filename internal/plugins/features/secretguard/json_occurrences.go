package secretguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

var errJSONOccurrenceMapping = errors.New("secretguard: JSON occurrence mapping failed")

// jsonStringMapping maps bytes in one decoded JSON string or raw scalar token
// back to the source byte interval that produced each decoded byte.
type jsonStringMapping struct {
	decoded    []byte
	boundaries []int
}

type jsonOccurrenceToken struct {
	mapping     jsonStringMapping
	stringValue bool
}

type jsonOccurrenceObjectEntry struct {
	key   jsonStringMapping
	value jsonOccurrenceValue
}

type jsonOccurrenceValue struct {
	token  *jsonOccurrenceToken
	object []jsonOccurrenceObjectEntry
	array  []jsonOccurrenceValue
}

// collectExactJSONOccurrences maps each semantic token that the canonical JSON
// scanner visits. It deliberately does not scan the raw JSON in addition to
// these tokens: punctuation, escape digits, and duplicate object entries are
// not independent canonical occurrences.
func collectExactJSONOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) []betterLeaksOccurrence {
	out, _ := collectExactJSONOccurrencesMapped(m, raw, fieldID)
	return out
}

func collectExactJSONOccurrencesMapped(m exactOccurrenceMatcher, raw []byte, fieldID string) ([]betterLeaksOccurrence, bool) {
	if m == nil || len(raw) == 0 {
		return nil, false
	}
	root, err := decodedJSONOccurrenceValue(raw)
	if err != nil {
		return nil, false
	}
	var tokens []jsonStringMapping
	root.semanticTokens(&tokens)
	return exactOccurrencesFromJSONTokens(m, tokens, raw, fieldID), true
}

func collectExactJSONRedactOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) ([]betterLeaksOccurrence, bool) {
	if m == nil || len(raw) == 0 {
		return nil, false
	}
	root, err := decodedJSONOccurrenceValue(raw)
	if err != nil {
		return nil, false
	}
	var tokens []jsonStringMapping
	root.redactSemanticTokens(m, &tokens)
	return exactOccurrencesFromJSONTokens(m, tokens, raw, fieldID), true
}

func exactOccurrencesFromJSONTokens(m exactOccurrenceMatcher, tokens []jsonStringMapping, raw []byte, fieldID string) []betterLeaksOccurrence {
	var out []betterLeaksOccurrence
	locationIndex := newBetterLeaksLocationIndex(raw)
	for _, token := range tokens {
		for _, occurrence := range m.ScanOccurrences(token.decoded) {
			if occurrence.Start < 0 || occurrence.End <= occurrence.Start || occurrence.End > len(token.decoded) {
				continue
			}
			rawStart, rawEnd, ok := token.rawRange(occurrence.Start, occurrence.End)
			if !ok {
				continue
			}
			span, err := spanForByteRangeWithLocationIndex(raw, locationIndex, rawStart, rawEnd)
			if err != nil {
				continue
			}
			out = append(out, betterLeaksOccurrence{
				value:          bytes.Clone(token.decoded[occurrence.Start:occurrence.End]),
				span:           span,
				start:          rawStart,
				end:            rawEnd,
				offsetsValid:   true,
				fieldID:        fieldID,
				ruleID:         occurrence.SecretRefName,
				role:           betterLeaksOccurrencePrimary,
				representation: betterLeaksOccurrenceDecoded,
			})
		}
	}
	return out
}

func (m jsonStringMapping) rawRange(start, end int) (int, int, bool) {
	if start < 0 || end <= start || end > len(m.decoded) || len(m.boundaries) != len(m.decoded)+1 {
		return 0, 0, false
	}
	rawStart, rawEnd := m.boundaries[start], m.boundaries[end]
	return rawStart, rawEnd, rawStart < rawEnd
}

// decodedJSONStringMappings retains the old values-only helper for callers
// that need string mappings. It follows the same first-value parse behavior as
// decodeJSONPreserveNumbers and intentionally ignores trailing content.
func decodedJSONStringMappings(raw []byte) ([]jsonStringMapping, error) {
	root, err := decodedJSONOccurrenceValue(raw)
	if err != nil {
		return nil, errJSONOccurrenceMapping
	}
	var out []jsonStringMapping
	root.stringValueMappings(&out)
	return out, nil
}

func decodedJSONOccurrenceValue(raw []byte) (jsonOccurrenceValue, error) {
	// Validate and bound the same first JSON value as canonical decoding.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var first any
	if err := dec.Decode(&first); err != nil {
		return jsonOccurrenceValue{}, errJSONOccurrenceMapping
	}
	parser := jsonOccurrenceParser{raw: raw[:int(dec.InputOffset())]}
	value, err := parser.semanticValue()
	if err != nil {
		return jsonOccurrenceValue{}, errJSONOccurrenceMapping
	}
	return value, nil
}

type jsonOccurrenceParser struct {
	raw []byte
	pos int
}

// value is retained as the values-only parser entry point used by the older
// helper. New occurrence collection uses semanticValue so object keys and
// scalar tokens share the canonical traversal order.
func (p *jsonOccurrenceParser) value(out *[]jsonStringMapping) error {
	value, err := p.semanticValue()
	if err != nil {
		return err
	}
	value.stringValueMappings(out)
	return nil
}

func (p *jsonOccurrenceParser) semanticValue() (jsonOccurrenceValue, error) {
	p.skipSpace()
	if p.pos >= len(p.raw) {
		return jsonOccurrenceValue{}, errJSONOccurrenceMapping
	}
	switch p.raw[p.pos] {
	case '"':
		mapping, err := p.string()
		if err != nil {
			return jsonOccurrenceValue{}, err
		}
		return jsonOccurrenceValue{token: &jsonOccurrenceToken{mapping: mapping, stringValue: true}}, nil
	case '{':
		return p.object()
	case '[':
		return p.array()
	default:
		mapping, err := p.literal()
		if err != nil {
			return jsonOccurrenceValue{}, err
		}
		return jsonOccurrenceValue{token: &jsonOccurrenceToken{mapping: mapping}}, nil
	}
}

func (p *jsonOccurrenceParser) object() (jsonOccurrenceValue, error) {
	p.pos++
	p.skipSpace()
	value := jsonOccurrenceValue{}
	if p.consume('}') {
		return value, nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.raw) || p.raw[p.pos] != '"' {
			return jsonOccurrenceValue{}, errJSONOccurrenceMapping
		}
		key, err := p.string()
		if err != nil {
			return jsonOccurrenceValue{}, err
		}
		p.skipSpace()
		if !p.consume(':') {
			return jsonOccurrenceValue{}, errJSONOccurrenceMapping
		}
		child, err := p.semanticValue()
		if err != nil {
			return jsonOccurrenceValue{}, err
		}
		value.object = append(value.object, jsonOccurrenceObjectEntry{key: key, value: child})
		p.skipSpace()
		if p.consume('}') {
			return value, nil
		}
		if !p.consume(',') {
			return jsonOccurrenceValue{}, errJSONOccurrenceMapping
		}
	}
}

func (p *jsonOccurrenceParser) array() (jsonOccurrenceValue, error) {
	p.pos++
	p.skipSpace()
	value := jsonOccurrenceValue{}
	if p.consume(']') {
		return value, nil
	}
	for {
		child, err := p.semanticValue()
		if err != nil {
			return jsonOccurrenceValue{}, err
		}
		value.array = append(value.array, child)
		p.skipSpace()
		if p.consume(']') {
			return value, nil
		}
		if !p.consume(',') {
			return jsonOccurrenceValue{}, errJSONOccurrenceMapping
		}
	}
}

func (p *jsonOccurrenceParser) literal() (jsonStringMapping, error) {
	start := p.pos
	dec := json.NewDecoder(bytes.NewReader(p.raw[start:]))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	switch value.(type) {
	case json.Number, bool, nil:
	default:
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	p.pos = start + int(dec.InputOffset())
	return rawJSONTokenMapping(p.raw, start, p.pos), nil
}

func rawJSONTokenMapping(raw []byte, start, end int) jsonStringMapping {
	decoded := bytes.Clone(raw[start:end])
	boundaries := make([]int, len(decoded)+1)
	for i := range decoded {
		boundaries[i] = start + i
	}
	boundaries[len(decoded)] = end
	return jsonStringMapping{decoded: decoded, boundaries: boundaries}
}

func (p *jsonOccurrenceParser) string() (jsonStringMapping, error) {
	if !p.consume('"') {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	contentStart := p.pos
	escaped := false
	for p.pos < len(p.raw) {
		b := p.raw[p.pos]
		if b == '"' && !escaped {
			contentEnd := p.pos
			p.pos++
			return decodeJSONStringMapping(p.raw, contentStart, contentEnd)
		}
		if b < 0x20 {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		if b == '\\' && !escaped {
			escaped = true
			p.pos++
			continue
		}
		escaped = false
		p.pos++
	}
	return jsonStringMapping{}, errJSONOccurrenceMapping
}

func decodeJSONStringMapping(raw []byte, start, end int) (jsonStringMapping, error) {
	token := make([]byte, 0, end-start+2)
	token = append(token, '"')
	token = append(token, raw[start:end]...)
	token = append(token, '"')
	var decoded string
	if err := json.Unmarshal(token, &decoded); err != nil {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}

	mapping := jsonStringMapping{decoded: make([]byte, 0, len(decoded)), boundaries: []int{start}}
	for pos := start; pos < end; {
		rawStart := pos
		if raw[pos] != '\\' {
			r, size := utf8.DecodeRune(raw[pos:end])
			if r == utf8.RuneError && size == 1 {
				mapping.append([]byte(string(utf8.RuneError)), rawStart, pos+1)
				pos++
				continue
			}
			mapping.append(raw[pos:pos+size], rawStart, pos+size)
			pos += size
			continue
		}
		if pos+1 >= end {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		if raw[pos+1] != 'u' {
			var value byte
			switch raw[pos+1] {
			case '"':
				value = '"'
			case '\\':
				value = '\\'
			case '/':
				value = '/'
			case 'b':
				value = '\b'
			case 'f':
				value = '\f'
			case 'n':
				value = '\n'
			case 'r':
				value = '\r'
			case 't':
				value = '\t'
			default:
				return jsonStringMapping{}, errJSONOccurrenceMapping
			}
			mapping.append([]byte{value}, rawStart, pos+2)
			pos += 2
			continue
		}
		if pos+6 > end {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		first, ok := jsonHexQuad(raw[pos+2 : pos+6])
		if !ok {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		pos += 6
		code := rune(first)
		if first >= 0xD800 && first <= 0xDBFF && pos+6 <= end && raw[pos] == '\\' && raw[pos+1] == 'u' {
			second, valid := jsonHexQuad(raw[pos+2 : pos+6])
			if valid && second >= 0xDC00 && second <= 0xDFFF {
				code = utf16.DecodeRune(rune(first), rune(second))
				pos += 6
			}
		}
		r := code
		if code >= 0xD800 && code <= 0xDFFF {
			r = utf8.RuneError
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], r)
		mapping.append(encoded[:n], rawStart, pos)
	}
	if !bytes.Equal(mapping.decoded, []byte(decoded)) {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	return mapping, nil
}

func (m *jsonStringMapping) append(value []byte, rawStart, rawEnd int) {
	m.boundaries[len(m.decoded)] = rawStart
	m.decoded = append(m.decoded, value...)
	for range value {
		m.boundaries = append(m.boundaries, rawEnd)
	}
}

func (v jsonOccurrenceValue) semanticTokens(out *[]jsonStringMapping) {
	if v.token != nil {
		*out = append(*out, v.token.mapping)
		return
	}
	if v.object != nil {
		effective := make(map[string]jsonOccurrenceObjectEntry, len(v.object))
		for _, entry := range v.object {
			effective[string(entry.key.decoded)] = entry
		}
		keys := make([]string, 0, len(effective))
		for key := range effective {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry := effective[key]
			*out = append(*out, entry.key)
			entry.value.semanticTokens(out)
		}
		return
	}
	for _, child := range v.array {
		child.semanticTokens(out)
	}
}

// redactSemanticTokens follows walkRedactJSON's effective-key ordering and
// fail-closed stop points. String values are rewritable, while an exact hit in
// a key or scalar is unsupported and stops traversal before later tokens.
func (v jsonOccurrenceValue) redactSemanticTokens(m exactOccurrenceMatcher, out *[]jsonStringMapping) bool {
	if v.token != nil {
		*out = append(*out, v.token.mapping)
		return !v.token.stringValue && len(m.ScanOccurrences(v.token.mapping.decoded)) > 0
	}
	if v.object != nil {
		effective := make(map[string]jsonOccurrenceObjectEntry, len(v.object))
		for _, entry := range v.object {
			effective[string(entry.key.decoded)] = entry
		}
		keys := make([]string, 0, len(effective))
		for key := range effective {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry := effective[key]
			*out = append(*out, entry.key)
			if len(m.ScanOccurrences(entry.key.decoded)) > 0 {
				return true
			}
			if entry.value.redactSemanticTokens(m, out) {
				return true
			}
		}
		return false
	}
	for _, child := range v.array {
		if child.redactSemanticTokens(m, out) {
			return true
		}
	}
	return false
}

func (v jsonOccurrenceValue) stringValueMappings(out *[]jsonStringMapping) {
	if v.token != nil {
		if v.token.stringValue {
			*out = append(*out, v.token.mapping)
		}
		return
	}
	if v.object != nil {
		for _, entry := range v.object {
			entry.value.stringValueMappings(out)
		}
		return
	}
	for _, child := range v.array {
		child.stringValueMappings(out)
	}
}

func jsonHexQuad(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var out uint16
	for _, b := range value {
		out <<= 4
		switch {
		case b >= '0' && b <= '9':
			out += uint16(b - '0')
		case b >= 'a' && b <= 'f':
			out += uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			out += uint16(b-'A') + 10
		default:
			return 0, false
		}
	}
	return out, true
}

func (p *jsonOccurrenceParser) skipSpace() {
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonOccurrenceParser) consume(want byte) bool {
	if p.pos >= len(p.raw) || p.raw[p.pos] != want {
		return false
	}
	p.pos++
	return true
}
