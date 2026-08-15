package providers

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

var forbiddenFixtureKeys = map[string]struct{}{
	"accesstoken":   {},
	"apikey":        {},
	"appkey":        {},
	"appsecret":     {},
	"authorization": {},
	"cookie":        {},
	"credential":    {},
	"password":      {},
	"privatekey":    {},
	"refreshtoken":  {},
	"secret":        {},
	"signature":     {},
	"token":         {},
}

func TestAdapterFixturesAreValidAndSanitized(t *testing.T) {
	t.Parallel()

	fixtureCount := 0
	for _, root := range []string{"bybit/testdata", "longbridge/testdata", "coingecko/testdata"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			fixtureCount++
			assertSanitizedJSONFixture(t, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk fixture directory %s: %v", root, err)
		}
	}
	if fixtureCount == 0 {
		t.Fatal("no adapter JSON fixtures found")
	}
}

func assertSanitizedJSONFixture(t *testing.T, path string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var document any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("fixture %s is not valid JSON: %v", path, err)
	}
	assertSanitizedFixtureValue(t, path, document)

	lowerContent := strings.ToLower(string(content))
	for _, marker := range []string{"bearer ", "-----begin private key-----", "longbridge_app_secret", "longbridge_access_token"} {
		if strings.Contains(lowerContent, marker) {
			t.Fatalf("fixture %s contains forbidden credential marker %q", path, marker)
		}
	}
}

func assertSanitizedFixtureValue(t *testing.T, path string, value any) {
	t.Helper()

	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, forbidden := forbiddenFixtureKeys[normalizeFixtureKey(key)]; forbidden {
				t.Fatalf("fixture %s contains forbidden credential field %q", path, key)
			}
			assertSanitizedFixtureValue(t, path, child)
		}
	case []any:
		for _, child := range typed {
			assertSanitizedFixtureValue(t, path, child)
		}
	}
}

func normalizeFixtureKey(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, value)
}
