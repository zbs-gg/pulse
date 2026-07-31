package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const maxCanonicalInteger = int64(9007199254740991)

var canonicalKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$`)

func CanonicalizeEnvelopeJSON(raw []byte, allowedTopLevel []string) ([]byte, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCanonicalValue(decoder)
	if err != nil {
		return nil, "", err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, "", errors.New("canonical_trailing_data")
		}
		return nil, "", fmt.Errorf("canonical_invalid_json:%w", err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, "", errors.New("canonical_root_must_be_object")
	}
	allowed := make(map[string]struct{}, len(allowedTopLevel))
	for _, key := range allowedTopLevel {
		allowed[norm.NFC.String(key)] = struct{}{}
	}
	for key := range root {
		if _, ok := allowed[norm.NFC.String(key)]; !ok {
			return nil, "", fmt.Errorf("canonical_unknown_field:%s", key)
		}
	}
	var out bytes.Buffer
	if err := writeCanonicalJSON(&out, root); err != nil {
		return nil, "", err
	}
	canonical := out.Bytes()
	sum := sha256.Sum256(canonical)
	return canonical, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeCanonicalValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("canonical_invalid_json:%w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("canonical_invalid_json:%w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("canonical_object_key_invalid")
				}
				normalized := norm.NFC.String(key)
				if !canonicalKey.MatchString(normalized) {
					return nil, fmt.Errorf("canonical_key_invalid:%s", normalized)
				}
				if _, exists := object[normalized]; exists {
					return nil, fmt.Errorf("canonical_duplicate_key:%s", normalized)
				}
				child, err := decodeCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				object[normalized] = child
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, errors.New("canonical_invalid_object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				child, err := decodeCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, errors.New("canonical_invalid_array")
			}
			return array, nil
		default:
			return nil, errors.New("canonical_invalid_delimiter")
		}
	case string:
		value = norm.NFC.String(value)
		if hasControl(value) {
			return nil, errors.New("canonical_control_character")
		}
		return value, nil
	case json.Number:
		integer, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil || integer > maxCanonicalInteger || integer < -maxCanonicalInteger {
			return nil, errors.New("canonical_number_not_safe_integer")
		}
		return integer, nil
	case bool, nil:
		return value, nil
	default:
		return nil, errors.New("canonical_value_invalid")
	}
}

func writeCanonicalJSON(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			keyJSON, _ := json.Marshal(key)
			out.Write(keyJSON)
			out.WriteByte(':')
			if err := writeCanonicalJSON(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonicalJSON(out, child); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		encoded, _ := json.Marshal(typed)
		out.Write(encoded)
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("canonical_value_invalid:%T", typed)
	}
	return nil
}

func canonicalErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return strings.SplitN(err.Error(), ":", 2)[0]
}
