// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cookiejar

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// tNow is the synthetic current time used as now during testing.
var tNow = time.Date(2013, 1, 1, 12, 0, 0, 0, time.UTC)

// testPSL implements PublicSuffixList with just two rules: "co.uk"
// and the default rule "*".
type testPSL struct{}

func (testPSL) String() string {
	return "testPSL"
}
func (testPSL) PublicSuffix(d string) string {
	if d == "co.uk" || strings.HasSuffix(d, ".co.uk") {
		return "co.uk"
	}
	return d[strings.LastIndex(d, ".")+1:]
}

// emptyPSL implements PublicSuffixList with just the default
// rule "*".
type emptyPSL struct{}

func (emptyPSL) String() string {
	return "emptyPSL"
}
func (emptyPSL) PublicSuffix(d string) string {
	return d[strings.LastIndex(d, ".")+1:]
}

// newTestJar creates an empty Jar with testPSL as the public suffix list.
func newTestJar(path string) *Jar {
	jar, err := New(&Options{
		PublicSuffixList: testPSL{},
		Filename:         path,
		NoPersist:        path == "",
	})
	if err != nil {
		panic(err)
	}
	return jar
}

var hasDotSuffixTests = [...]struct {
	s, suffix string
}{
	{"", ""},
	{"", "."},
	{"", "x"},
	{".", ""},
	{".", "."},
	{".", ".."},
	{".", "x"},
	{".", "x."},
	{".", ".x"},
	{".", ".x."},
	{"x", ""},
	{"x", "."},
	{"x", ".."},
	{"x", "x"},
	{"x", "x."},
	{"x", ".x"},
	{"x", ".x."},
	{".x", ""},
	{".x", "."},
	{".x", ".."},
	{".x", "x"},
	{".x", "x."},
	{".x", ".x"},
	{".x", ".x."},
	{"x.", ""},
	{"x.", "."},
	{"x.", ".."},
	{"x.", "x"},
	{"x.", "x."},
	{"x.", ".x"},
	{"x.", ".x."},
	{"com", ""},
	{"com", "m"},
	{"com", "om"},
	{"com", "com"},
	{"com", ".com"},
	{"com", "x.com"},
	{"com", "xcom"},
	{"com", "xorg"},
	{"com", "org"},
	{"com", "rg"},
	{"foo.com", ""},
	{"foo.com", "m"},
	{"foo.com", "om"},
	{"foo.com", "com"},
	{"foo.com", ".com"},
	{"foo.com", "o.com"},
	{"foo.com", "oo.com"},
	{"foo.com", "foo.com"},
	{"foo.com", ".foo.com"},
	{"foo.com", "x.foo.com"},
	{"foo.com", "xfoo.com"},
	{"foo.com", "xfoo.org"},
	{"foo.com", "foo.org"},
	{"foo.com", "oo.org"},
	{"foo.com", "o.org"},
	{"foo.com", ".org"},
	{"foo.com", "org"},
	{"foo.com", "rg"},
}

func TestHasDotSuffix(t *testing.T) {
	for _, tc := range hasDotSuffixTests {
		got := hasDotSuffix(tc.s, tc.suffix)
		want := strings.HasSuffix(tc.s, "."+tc.suffix)
		if got != want {
			t.Errorf("s=%q, suffix=%q: got %v, want %v", tc.s, tc.suffix, got, want)
		}
	}
}

var canonicalHostTests = map[string]string{
	"www.example.com":         "www.example.com",
	"WWW.EXAMPLE.COM":         "www.example.com",
	"wWw.eXAmple.CoM":         "www.example.com",
	"www.example.com:80":      "www.example.com",
	"192.168.0.10":            "192.168.0.10",
	"192.168.0.5:8080":        "192.168.0.5",
	"2001:4860:0:2001::68":    "2001:4860:0:2001::68",
	"[2001:4860:0:::68]:8080": "2001:4860:0:::68",
	"www.bücher.de":           "www.xn--bcher-kva.de",
	"www.example.com.":        "www.example.com",
	"[bad.unmatched.bracket:": "error",
}

func TestCanonicalHost(t *testing.T) {
	for h, want := range canonicalHostTests {
		got, err := canonicalHost(h)
		if want == "error" {
			if err == nil {
				t.Errorf("%q: got nil error, want non-nil", h)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", h, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", h, got, want)
			continue
		}
	}
}

var hasPortTests = map[string]bool{
	"www.example.com":      false,
	"www.example.com:80":   true,
	"127.0.0.1":            false,
	"127.0.0.1:8080":       true,
	"2001:4860:0:2001::68": false,
	"[2001::0:::68]:80":    true,
}

func TestHasPort(t *testing.T) {
	for host, want := range hasPortTests {
		if got := hasPort(host); got != want {
			t.Errorf("%q: got %t, want %t", host, got, want)
		}
	}
}

var jarKeyTests = map[string]string{
	"foo.www.example.com": "example.com",
	"www.example.com":     "example.com",
	"example.com":         "example.com",
	"com":                 "com",
	"foo.www.bbc.co.uk":   "bbc.co.uk",
	"www.bbc.co.uk":       "bbc.co.uk",
	"bbc.co.uk":           "bbc.co.uk",
	"co.uk":               "co.uk",
	"uk":                  "uk",
	"192.168.0.5":         "192.168.0.5",
}

func TestJarKey(t *testing.T) {
	for host, want := range jarKeyTests {
		if got := jarKey(host, testPSL{}); got != want {
			t.Errorf("%q: got %q, want %q", host, got, want)
		}
	}
}

var jarKeyNilPSLTests = map[string]string{
	"foo.www.example.com": "example.com",
	"www.example.com":     "example.com",
	"example.com":         "example.com",
	"com":                 "com",
	"foo.www.bbc.co.uk":   "co.uk",
	"www.bbc.co.uk":       "co.uk",
	"bbc.co.uk":           "co.uk",
	"co.uk":               "co.uk",
	"uk":                  "uk",
	"192.168.0.5":         "192.168.0.5",
}

func TestJarKeyNilPSL(t *testing.T) {
	for host, want := range jarKeyNilPSLTests {
		if got := jarKey(host, nil); got != want {
			t.Errorf("%q: got %q, want %q", host, got, want)
		}
	}
}

var isIPTests = map[string]bool{
	"127.0.0.1":            true,
	"1.2.3.4":              true,
	"2001:4860:0:2001::68": true,
	"example.com":          false,
	"1.1.1.300":            false,
	"www.foo.bar.net":      false,
	"123.foo.bar.net":      false,
}

func TestIsIP(t *testing.T) {
	for host, want := range isIPTests {
		if got := isIP(host); got != want {
			t.Errorf("%q: got %t, want %t", host, got, want)
		}
	}
}

var defaultPathTests = map[string]string{
	"/":           "/",
	"/abc":        "/",
	"/abc/":       "/abc",
	"/abc/xyz":    "/abc",
	"/abc/xyz/":   "/abc/xyz",
	"/a/b/c.html": "/a/b",
	"":            "/",
	"strange":     "/",
	"//":          "/",
	"/a//b":       "/a/",
	"/a/./b":      "/a/.",
	"/a/../b":     "/a/..",
}

func TestDefaultPath(t *testing.T) {
	for path, want := range defaultPathTests {
		if got := defaultPath(path); got != want {
			t.Errorf("%q: got %q, want %q", path, got, want)
		}
	}
}

var domainAndTypeTests = [...]struct {
	host         string // host Set-Cookie header was received from
	domain       string // domain attribute in Set-Cookie header
	wantDomain   string // expected domain of cookie
	wantHostOnly bool   // expected host-cookie flag
	wantErr      error  // expected error
}{
	{"www.example.com", "", "www.example.com", true, nil},
	{"127.0.0.1", "", "127.0.0.1", true, nil},
	{"2001:4860:0:2001::68", "", "2001:4860:0:2001::68", true, nil},
	{"www.example.com", "example.com", "example.com", false, nil},
	{"www.example.com", ".example.com", "example.com", false, nil},
	{"www.example.com", "www.example.com", "www.example.com", false, nil},
	{"www.example.com", ".www.example.com", "www.example.com", false, nil},
	{"foo.sso.example.com", "sso.example.com", "sso.example.com", false, nil},
	{"bar.co.uk", "bar.co.uk", "bar.co.uk", false, nil},
	{"foo.bar.co.uk", ".bar.co.uk", "bar.co.uk", false, nil},
	{"127.0.0.1", "127.0.0.1", "", false, errNoHostname},
	{"2001:4860:0:2001::68", "2001:4860:0:2001::68", "2001:4860:0:2001::68", false, errNoHostname},
	{"www.example.com", ".", "", false, errMalformedDomain},
	{"www.example.com", "..", "", false, errMalformedDomain},
	{"www.example.com", "other.com", "", false, errIllegalDomain},
	{"www.example.com", "com", "", false, errIllegalDomain},
	{"www.example.com", ".com", "", false, errIllegalDomain},
	{"foo.bar.co.uk", ".co.uk", "", false, errIllegalDomain},
	{"127.www.0.0.1", "127.0.0.1", "", false, errIllegalDomain},
	{"com", "", "com", true, nil},
	{"com", "com", "com", true, nil},
	{"com", ".com", "com", true, nil},
	{"co.uk", "", "co.uk", true, nil},
	{"co.uk", "co.uk", "co.uk", true, nil},
	{"co.uk", ".co.uk", "co.uk", true, nil},
}

func TestDomainAndType(t *testing.T) {
	jar := newTestJar("")
	for _, tc := range domainAndTypeTests {
		domain, hostOnly, err := jar.domainAndType(tc.host, tc.domain)
		if err != tc.wantErr {
			t.Errorf("%q/%q: got %q error, want %q",
				tc.host, tc.domain, err, tc.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if domain != tc.wantDomain || hostOnly != tc.wantHostOnly {
			t.Errorf("%q/%q: got %q/%t want %q/%t",
				tc.host, tc.domain, domain, hostOnly,
				tc.wantDomain, tc.wantHostOnly)
		}
	}
}

// basicsTests contains fundamental tests. Each jarTest has to be performed on
// a fresh, empty Jar.
var basicsTests = [...]jarTest{
	{
		"Retrieval of a plain host cookie.",
		"http://www.host.test/",
		[]string{"A=a"},
		"A=a",
		[]query{
			{"http://www.host.test", "A=a"},
			{"http://www.host.test/", "A=a"},
			{"http://www.host.test/some/path", "A=a"},
			{"https://www.host.test", "A=a"},
			{"https://www.host.test/", "A=a"},
			{"https://www.host.test/some/path", "A=a"},
			{"ftp://www.host.test", ""},
			{"ftp://www.host.test/", ""},
			{"ftp://www.host.test/some/path", ""},
			{"http://www.other.org", ""},
			{"http://sibling.host.test", ""},
			{"http://deep.www.host.test", ""},
		},
	},
	{
		"Secure cookies are not returned to http.",
		"http://www.host.test/",
		[]string{"A=a; secure"},
		"A=a",
		[]query{
			{"http://www.host.test", ""},
			{"http://www.host.test/", ""},
			{"http://www.host.test/some/path", ""},
			{"https://www.host.test", "A=a"},
			{"https://www.host.test/", "A=a"},
			{"https://www.host.test/some/path", "A=a"},
		},
	},
	{
		"Explicit path.",
		"http://www.host.test/",
		[]string{"A=a; path=/some/path"},
		"A=a",
		[]query{
			{"http://www.host.test", ""},
			{"http://www.host.test/", ""},
			{"http://www.host.test/some", ""},
			{"http://www.host.test/some/", ""},
			{"http://www.host.test/some/path", "A=a"},
			{"http://www.host.test/some/paths", ""},
			{"http://www.host.test/some/path/foo", "A=a"},
			{"http://www.host.test/some/path/foo/", "A=a"},
		},
	},
	{
		"Implicit path #1: path is a directory.",
		"http://www.host.test/some/path/",
		[]string{"A=a"},
		"A=a",
		[]query{
			{"http://www.host.test", ""},
			{"http://www.host.test/", ""},
			{"http://www.host.test/some", ""},
			{"http://www.host.test/some/", ""},
			{"http://www.host.test/some/path", "A=a"},
			{"http://www.host.test/some/paths", ""},
			{"http://www.host.test/some/path/foo", "A=a"},
			{"http://www.host.test/some/path/foo/", "A=a"},
		},
	},
	{
		"Implicit path #2: path is not a directory.",
		"http://www.host.test/some/path/index.html",
		[]string{"A=a"},
		"A=a",
		[]query{
			{"http://www.host.test", ""},
			{"http://www.host.test/", ""},
			{"http://www.host.test/some", ""},
			{"http://www.host.test/some/", ""},
			{"http://www.host.test/some/path", "A=a"},
			{"http://www.host.test/some/paths", ""},
			{"http://www.host.test/some/path/foo", "A=a"},
			{"http://www.host.test/some/path/foo/", "A=a"},
		},
	},
	{
		"Implicit path #3: no path in URL at all.",
		"http://www.host.test",
		[]string{"A=a"},
		"A=a",
		[]query{
			{"http://www.host.test", "A=a"},
			{"http://www.host.test/", "A=a"},
			{"http://www.host.test/some/path", "A=a"},
		},
	},
	{
		"Cookies are sorted by path length.",
		"http://www.host.test/",
		[]string{
			"A=a; path=/foo/bar",
			"B=b; path=/foo/bar/baz/qux",
			"C=c; path=/foo/bar/baz",
			"D=d; path=/foo"},
		"A=a B=b C=c D=d",
		[]query{
			{"http://www.host.test/foo/bar/baz/qux", "B=b C=c A=a D=d"},
			{"http://www.host.test/foo/bar/baz/", "C=c A=a D=d"},
			{"http://www.host.test/foo/bar", "A=a D=d"},
		},
	},
	// TODO fix this test. It has never actually tested sorting on
	// creation time because all the cookies are actually created at
	// the same moment in time.
	//	{
	//		"Creation time determines sorting on same length paths.",
	//		"http://www.host.test/",
	//		[]string{
	//			"A=a; path=/foo/bar",
	//			"X=x; path=/foo/bar",
	//			"Y=y; path=/foo/bar/baz/qux",
	//			"B=b; path=/foo/bar/baz/qux",
	//			"C=c; path=/foo/bar/baz",
	//			"W=w; path=/foo/bar/baz",
	//			"Z=z; path=/foo",
	//			"D=d; path=/foo"},
	//		"A=a B=b C=c D=d W=w X=x Y=y Z=z",
	//		[]query{
	//			{"http://www.host.test/foo/bar/baz/qux", "Y=y B=b C=c W=w A=a X=x Z=z D=d"},
	//			{"http://www.host.test/foo/bar/baz/", "C=c W=w A=a X=x Z=z D=d"},
	//			{"http://www.host.test/foo/bar", "A=a X=x Z=z D=d"},
	//		},
	//	},
	{
		"Sorting of same-name cookies.",
		"http://www.host.test/",
		[]string{
			"A=1; path=/",
			"A=2; path=/path",
			"A=3; path=/quux",
			"A=4; path=/path/foo",
			"A=5; domain=.host.test; path=/path",
			"A=6; domain=.host.test; path=/quux",
			"A=7; domain=.host.test; path=/path/foo",
		},
		"A=1 A=2 A=3 A=4 A=5 A=6 A=7",
		[]query{
			{"http://www.host.test/path", "A=2 A=5 A=1"},
			{"http://www.host.test/path/foo", "A=4 A=7 A=2 A=5 A=1"},
		},
	},
	{
		"Disallow domain cookie on public suffix.",
		"http://www.bbc.co.uk",
		[]string{
			"a=1",
			"b=2; domain=co.uk",
		},
		"a=1",
		[]query{{"http://www.bbc.co.uk", "a=1"}},
	},
	{
		"Host cookie on IP.",
		"http://192.168.0.10",
		[]string{"a=1"},
		"a=1",
		[]query{{"http://192.168.0.10", "a=1"}},
	},
	{
		"Port is ignored #1.",
		"http://www.host.test/",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://www.host.test", "a=1"},
			{"http://www.host.test:8080/", "a=1"},
		},
	},
	{
		"Port is ignored #2.",
		"http://www.host.test:8080/",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://www.host.test", "a=1"},
			{"http://www.host.test:8080/", "a=1"},
			{"http://www.host.test:1234/", "a=1"},
		},
	},
}

func TestBasics(t *testing.T) {
	for _, test := range basicsTests {
		jar := newTestJar("")
		test.run(t, jar)
	}
}

// updateAndDeleteTests contains jarTests which must be performed on the same
// Jar.
var updateAndDeleteTests = [...]jarTest{
	{
		"Set initial cookies.",
		"http://www.host.test",
		[]string{
			"a=1",
			"b=2; secure",
			"c=3; httponly",
			"d=4; secure; httponly"},
		"a=1 b=2 c=3 d=4",
		[]query{
			{"http://www.host.test", "a=1 c=3"},
			{"https://www.host.test", "a=1 b=2 c=3 d=4"},
		},
	},
	{
		"Update value via http.",
		"http://www.host.test",
		[]string{
			"a=w",
			"b=x; secure",
			"c=y; httponly",
			"d=z; secure; httponly"},
		"a=w b=x c=y d=z",
		[]query{
			{"http://www.host.test", "a=w c=y"},
			{"https://www.host.test", "a=w b=x c=y d=z"},
		},
	},
	{
		"Clear Secure flag from a http.",
		"http://www.host.test/",
		[]string{
			"b=xx",
			"d=zz; httponly"},
		"a=w b=xx c=y d=zz",
		[]query{{"http://www.host.test", "a=w b=xx c=y d=zz"}},
	},
	{
		"Delete all.",
		"http://www.host.test/",
		[]string{
			"a=1; max-Age=-1",                    // delete via MaxAge
			"b=2; " + expiresIn(-10),             // delete via Expires
			"c=2; max-age=-1; " + expiresIn(-10), // delete via both
			"d=4; max-age=-1; " + expiresIn(10)}, // MaxAge takes precedence
		"",
		[]query{{"http://www.host.test", ""}},
	},
	{
		"Refill #1.",
		"http://www.host.test",
		[]string{
			"A=1",
			"A=2; path=/foo",
			"A=3; domain=.host.test",
			"A=4; path=/foo; domain=.host.test"},
		"A=1 A=2 A=3 A=4",
		[]query{{"http://www.host.test/foo", "A=2 A=4 A=1 A=3"}},
	},
	{
		"Refill #2.",
		"http://www.google.com",
		[]string{
			"A=6",
			"A=7; path=/foo",
			"A=8; domain=.google.com",
			"A=9; path=/foo; domain=.google.com"},
		"A=1 A=2 A=3 A=4 A=6 A=7 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=4 A=1 A=3"},
			{"http://www.google.com/foo", "A=7 A=9 A=6 A=8"},
		},
	},
	{
		"Delete A7.",
		"http://www.google.com",
		[]string{"A=; path=/foo; max-age=-1"},
		"A=1 A=2 A=3 A=4 A=6 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=4 A=1 A=3"},
			{"http://www.google.com/foo", "A=9 A=6 A=8"},
		},
	},
	{
		"Delete A4.",
		"http://www.host.test",
		[]string{"A=; path=/foo; domain=host.test; max-age=-1"},
		"A=1 A=2 A=3 A=6 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=1 A=3"},
			{"http://www.google.com/foo", "A=9 A=6 A=8"},
		},
	},
	{
		"Delete A6.",
		"http://www.google.com",
		[]string{"A=; max-age=-1"},
		"A=1 A=2 A=3 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=1 A=3"},
			{"http://www.google.com/foo", "A=9 A=8"},
		},
	},
	{
		"Delete A3.",
		"http://www.host.test",
		[]string{"A=; domain=host.test; max-age=-1"},
		"A=1 A=2 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=1"},
			{"http://www.google.com/foo", "A=9 A=8"},
		},
	},
	{
		"No cross-domain delete.",
		"http://www.host.test",
		[]string{
			"A=; domain=google.com; max-age=-1",
			"A=; path=/foo; domain=google.com; max-age=-1"},
		"A=1 A=2 A=8 A=9",
		[]query{
			{"http://www.host.test/foo", "A=2 A=1"},
			{"http://www.google.com/foo", "A=9 A=8"},
		},
	},
	{
		"Delete A8 and A9.",
		"http://www.google.com",
		[]string{
			"A=; domain=google.com; max-age=-1",
			"A=; path=/foo; domain=google.com; max-age=-1"},
		"A=1 A=2",
		[]query{
			{"http://www.host.test/foo", "A=2 A=1"},
			{"http://www.google.com/foo", ""},
		},
	},
}

func TestUpdateAndDelete(t *testing.T) {
	jar := newTestJar("")
	for _, test := range updateAndDeleteTests {
		test.run(t, jar)
	}
}

func TestExpiration(t *testing.T) {
	jar := newTestJar("")
	jarTest{
		"Expiration.",
		"http://www.host.test",
		[]string{
			"a=1",
			"b=2; max-age=3",
			"c=3; " + expiresIn(3),
			"d=4; max-age=5",
			"e=5; " + expiresIn(5),
			"f=6; max-age=100",
		},
		"a=1 b=2 c=3 d=4 e=5 f=6", // executed at t0 + 1001 ms
		[]query{
			{"http://www.host.test", "a=1 b=2 c=3 d=4 e=5 f=6"}, // t0 + 2002 ms
			{"http://www.host.test", "a=1 d=4 e=5 f=6"},         // t0 + 3003 ms
			{"http://www.host.test", "a=1 d=4 e=5 f=6"},         // t0 + 4004 ms
			{"http://www.host.test", "a=1 f=6"},                 // t0 + 5005 ms
			{"http://www.host.test", "a=1 f=6"},                 // t0 + 6006 ms
		},
	}.run(t, jar)
}

//
// Tests derived from Chromium's cookie_store_unittest.h.
//

// See http://src.chromium.org/viewvc/chrome/trunk/src/net/cookies/cookie_store_unittest.h?revision=159685&content-type=text/plain
// Some of the original tests are in a bad condition (e.g.
// DomainWithTrailingDotTest) or are not RFC 6265 conforming (e.g.
// TestNonDottedAndTLD #1 and #6) and have not been ported.

// chromiumBasicsTests contains fundamental tests. Each jarTest has to be
// performed on a fresh, empty Jar.
var chromiumBasicsTests = [...]jarTest{
	{
		"DomainWithTrailingDotTest.",
		"http://www.google.com/",
		[]string{
			"a=1; domain=.www.google.com.",
			"b=2; domain=.www.google.com..",
		},
		"",
		[]query{
			{"http://www.google.com", ""},
		},
	},
	{
		"ValidSubdomainTest #1.",
		"http://a.b.c.d.com",
		[]string{
			"a=1; domain=.a.b.c.d.com",
			"b=2; domain=.b.c.d.com",
			"c=3; domain=.c.d.com",
			"d=4; domain=.d.com",
		},
		"a=1 b=2 c=3 d=4",
		[]query{
			{"http://a.b.c.d.com", "a=1 b=2 c=3 d=4"},
			{"http://b.c.d.com", "b=2 c=3 d=4"},
			{"http://c.d.com", "c=3 d=4"},
			{"http://d.com", "d=4"},
		},
	},
	{
		"ValidSubdomainTest #2.",
		"http://a.b.c.d.com",
		[]string{
			"a=1; domain=.a.b.c.d.com",
			"b=2; domain=.b.c.d.com",
			"c=3; domain=.c.d.com",
			"d=4; domain=.d.com",
			"X=bcd; domain=.b.c.d.com",
			"X=cd; domain=.c.d.com"},
		"X=bcd X=cd a=1 b=2 c=3 d=4",
		[]query{
			{"http://b.c.d.com", "X=bcd X=cd b=2 c=3 d=4"},
			{"http://c.d.com", "X=cd c=3 d=4"},
		},
	},
	{
		"InvalidDomainTest #1.",
		"http://foo.bar.com",
		[]string{
			"a=1; domain=.yo.foo.bar.com",
			"b=2; domain=.foo.com",
			"c=3; domain=.bar.foo.com",
			"d=4; domain=.foo.bar.com.net",
			"e=5; domain=ar.com",
			"f=6; domain=.",
			"g=7; domain=/",
			"h=8; domain=http://foo.bar.com",
			"i=9; domain=..foo.bar.com",
			"j=10; domain=..bar.com",
			"k=11; domain=.foo.bar.com?blah",
			"l=12; domain=.foo.bar.com/blah",
			"m=12; domain=.foo.bar.com:80",
			"n=14; domain=.foo.bar.com:",
			"o=15; domain=.foo.bar.com#sup",
		},
		"", // Jar is empty.
		[]query{{"http://foo.bar.com", ""}},
	},
	{
		"InvalidDomainTest #2.",
		"http://foo.com.com",
		[]string{"a=1; domain=.foo.com.com.com"},
		"",
		[]query{{"http://foo.bar.com", ""}},
	},
	{
		"DomainWithoutLeadingDotTest #1.",
		"http://manage.hosted.filefront.com",
		[]string{"a=1; domain=filefront.com"},
		"a=1",
		[]query{{"http://www.filefront.com", "a=1"}},
	},
	{
		"DomainWithoutLeadingDotTest #2.",
		"http://www.google.com",
		[]string{"a=1; domain=www.google.com"},
		"a=1",
		[]query{
			{"http://www.google.com", "a=1"},
			{"http://sub.www.google.com", "a=1"},
			{"http://something-else.com", ""},
		},
	},
	{
		"CaseInsensitiveDomainTest.",
		"http://www.google.com",
		[]string{
			"a=1; domain=.GOOGLE.COM",
			"b=2; domain=.www.gOOgLE.coM"},
		"a=1 b=2",
		[]query{{"http://www.google.com", "a=1 b=2"}},
	},
	{
		"TestIpAddress #1.",
		"http://1.2.3.4/foo",
		[]string{"a=1; path=/"},
		"a=1",
		[]query{{"http://1.2.3.4/foo", "a=1"}},
	},
	{
		"TestIpAddress #2.",
		"http://1.2.3.4/foo",
		[]string{
			"a=1; domain=.1.2.3.4",
			"b=2; domain=.3.4"},
		"",
		[]query{{"http://1.2.3.4/foo", ""}},
	},
	{
		"TestIpAddress #3.",
		"http://1.2.3.4/foo",
		[]string{"a=1; domain=1.2.3.4"},
		"",
		[]query{{"http://1.2.3.4/foo", ""}},
	},
	{
		"TestNonDottedAndTLD #2.",
		"http://com./index.html",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://com./index.html", "a=1"},
			{"http://no-cookies.com./index.html", ""},
		},
	},
	{
		"TestNonDottedAndTLD #3.",
		"http://a.b",
		[]string{
			"a=1; domain=.b",
			"b=2; domain=b"},
		"",
		[]query{{"http://bar.foo", ""}},
	},
	{
		"TestNonDottedAndTLD #4.",
		"http://google.com",
		[]string{
			"a=1; domain=.com",
			"b=2; domain=com"},
		"",
		[]query{{"http://google.com", ""}},
	},
	{
		"TestNonDottedAndTLD #5.",
		"http://google.co.uk",
		[]string{
			"a=1; domain=.co.uk",
			"b=2; domain=.uk"},
		"",
		[]query{
			{"http://google.co.uk", ""},
			{"http://else.co.com", ""},
			{"http://else.uk", ""},
		},
	},
	{
		"TestHostEndsWithDot.",
		"http://www.google.com",
		[]string{
			"a=1",
			"b=2; domain=.www.google.com."},
		"a=1",
		[]query{{"http://www.google.com", "a=1"}},
	},
	{
		"PathTest",
		"http://www.google.izzle",
		[]string{"a=1; path=/wee"},
		"a=1",
		[]query{
			{"http://www.google.izzle/wee", "a=1"},
			{"http://www.google.izzle/wee/", "a=1"},
			{"http://www.google.izzle/wee/war", "a=1"},
			{"http://www.google.izzle/wee/war/more/more", "a=1"},
			{"http://www.google.izzle/weehee", ""},
			{"http://www.google.izzle/", ""},
		},
	},
}

func TestChromiumBasics(t *testing.T) {
	for _, test := range chromiumBasicsTests {
		jar := newTestJar("")
		test.run(t, jar)
	}
}

// chromiumDomainTests contains jarTests which must be executed all on the
// same Jar.
var chromiumDomainTests = [...]jarTest{
	{
		"Fill #1.",
		"http://www.google.izzle",
		[]string{"A=B"},
		"A=B",
		[]query{{"http://www.google.izzle", "A=B"}},
	},
	{
		"Fill #2.",
		"http://www.google.izzle",
		[]string{"C=D; domain=.google.izzle"},
		"A=B C=D",
		[]query{{"http://www.google.izzle", "A=B C=D"}},
	},
	{
		"Verify A is a host cookie and not accessible from subdomain.",
		"http://unused.nil",
		[]string{},
		"A=B C=D",
		[]query{{"http://foo.www.google.izzle", "C=D"}},
	},
	{
		"Verify domain cookies are found on proper domain.",
		"http://www.google.izzle",
		[]string{"E=F; domain=.www.google.izzle"},
		"A=B C=D E=F",
		[]query{{"http://www.google.izzle", "A=B C=D E=F"}},
	},
	{
		"Leading dots in domain attributes are optional.",
		"http://www.google.izzle",
		[]string{"G=H; domain=www.google.izzle"},
		"A=B C=D E=F G=H",
		[]query{{"http://www.google.izzle", "A=B C=D E=F G=H"}},
	},
	{
		"Verify domain enforcement works #1.",
		"http://www.google.izzle",
		[]string{"K=L; domain=.bar.www.google.izzle"},
		"A=B C=D E=F G=H",
		[]query{{"http://bar.www.google.izzle", "C=D E=F G=H"}},
	},
	{
		"Verify domain enforcement works #2.",
		"http://unused.nil",
		[]string{},
		"A=B C=D E=F G=H",
		[]query{{"http://www.google.izzle", "A=B C=D E=F G=H"}},
	},
}

func TestChromiumDomain(t *testing.T) {
	jar := newTestJar("")
	for _, test := range chromiumDomainTests {
		test.run(t, jar)
	}

}

// chromiumDeletionTests must be performed all on the same Jar.
var chromiumDeletionTests = [...]jarTest{
	{
		"Create session cookie a1.",
		"http://www.google.com",
		[]string{"a=1"},
		"a=1",
		[]query{{"http://www.google.com", "a=1"}},
	},
	{
		"Delete sc a1 via MaxAge.",
		"http://www.google.com",
		[]string{"a=1; max-age=-1"},
		"",
		[]query{{"http://www.google.com", ""}},
	},
	{
		"Create session cookie b2.",
		"http://www.google.com",
		[]string{"b=2"},
		"b=2",
		[]query{{"http://www.google.com", "b=2"}},
	},
	{
		"Delete sc b2 via Expires.",
		"http://www.google.com",
		[]string{"b=2; " + expiresIn(-10)},
		"",
		[]query{{"http://www.google.com", ""}},
	},
	{
		"Create persistent cookie c3.",
		"http://www.google.com",
		[]string{"c=3; max-age=3600"},
		"c=3",
		[]query{{"http://www.google.com", "c=3"}},
	},
	{
		"Delete pc c3 via MaxAge.",
		"http://www.google.com",
		[]string{"c=3; max-age=-1"},
		"",
		[]query{{"http://www.google.com", ""}},
	},
	{
		"Create persistent cookie d4.",
		"http://www.google.com",
		[]string{"d=4; max-age=3600"},
		"d=4",
		[]query{{"http://www.google.com", "d=4"}},
	},
	{
		"Delete pc d4 via Expires.",
		"http://www.google.com",
		[]string{"d=4; " + expiresIn(-10)},
		"",
		[]query{{"http://www.google.com", ""}},
	},
}

func TestChromiumDeletion(t *testing.T) {
	jar := newTestJar("")
	for _, test := range chromiumDeletionTests {
		test.run(t, jar)
	}
}

// domainHandlingTests tests and documents the rules for domain handling.
// Each test must be performed on an empty new Jar.
var domainHandlingTests = [...]jarTest{
	{
		"Host cookie",
		"http://www.host.test",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://www.host.test", "a=1"},
			{"http://host.test", ""},
			{"http://bar.host.test", ""},
			{"http://foo.www.host.test", ""},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Domain cookie #1",
		"http://www.host.test",
		[]string{"a=1; domain=host.test"},
		"a=1",
		[]query{
			{"http://www.host.test", "a=1"},
			{"http://host.test", "a=1"},
			{"http://bar.host.test", "a=1"},
			{"http://foo.www.host.test", "a=1"},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Domain cookie #2",
		"http://www.host.test",
		[]string{"a=1; domain=.host.test"},
		"a=1",
		[]query{
			{"http://www.host.test", "a=1"},
			{"http://host.test", "a=1"},
			{"http://bar.host.test", "a=1"},
			{"http://foo.www.host.test", "a=1"},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Host cookie on IDNA domain #1",
		"http://www.bücher.test",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://www.bücher.test", "a=1"},
			{"http://www.xn--bcher-kva.test", "a=1"},
			{"http://bücher.test", ""},
			{"http://xn--bcher-kva.test", ""},
			{"http://bar.bücher.test", ""},
			{"http://bar.xn--bcher-kva.test", ""},
			{"http://foo.www.bücher.test", ""},
			{"http://foo.www.xn--bcher-kva.test", ""},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Host cookie on IDNA domain #2",
		"http://www.xn--bcher-kva.test",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://www.bücher.test", "a=1"},
			{"http://www.xn--bcher-kva.test", "a=1"},
			{"http://bücher.test", ""},
			{"http://xn--bcher-kva.test", ""},
			{"http://bar.bücher.test", ""},
			{"http://bar.xn--bcher-kva.test", ""},
			{"http://foo.www.bücher.test", ""},
			{"http://foo.www.xn--bcher-kva.test", ""},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Domain cookie on IDNA domain #1",
		"http://www.bücher.test",
		[]string{"a=1; domain=xn--bcher-kva.test"},
		"a=1",
		[]query{
			{"http://www.bücher.test", "a=1"},
			{"http://www.xn--bcher-kva.test", "a=1"},
			{"http://bücher.test", "a=1"},
			{"http://xn--bcher-kva.test", "a=1"},
			{"http://bar.bücher.test", "a=1"},
			{"http://bar.xn--bcher-kva.test", "a=1"},
			{"http://foo.www.bücher.test", "a=1"},
			{"http://foo.www.xn--bcher-kva.test", "a=1"},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Domain cookie on IDNA domain #2",
		"http://www.xn--bcher-kva.test",
		[]string{"a=1; domain=xn--bcher-kva.test"},
		"a=1",
		[]query{
			{"http://www.bücher.test", "a=1"},
			{"http://www.xn--bcher-kva.test", "a=1"},
			{"http://bücher.test", "a=1"},
			{"http://xn--bcher-kva.test", "a=1"},
			{"http://bar.bücher.test", "a=1"},
			{"http://bar.xn--bcher-kva.test", "a=1"},
			{"http://foo.www.bücher.test", "a=1"},
			{"http://foo.www.xn--bcher-kva.test", "a=1"},
			{"http://other.test", ""},
			{"http://test", ""},
		},
	},
	{
		"Host cookie on TLD.",
		"http://com",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://com", "a=1"},
			{"http://any.com", ""},
			{"http://any.test", ""},
		},
	},
	{
		"Domain cookie on TLD becomes a host cookie.",
		"http://com",
		[]string{"a=1; domain=com"},
		"a=1",
		[]query{
			{"http://com", "a=1"},
			{"http://any.com", ""},
			{"http://any.test", ""},
		},
	},
	{
		"Host cookie on public suffix.",
		"http://co.uk",
		[]string{"a=1"},
		"a=1",
		[]query{
			{"http://co.uk", "a=1"},
			{"http://uk", ""},
			{"http://some.co.uk", ""},
			{"http://foo.some.co.uk", ""},
			{"http://any.uk", ""},
		},
	},
	{
		"Domain cookie on public suffix is ignored.",
		"http://some.co.uk",
		[]string{"a=1; domain=co.uk"},
		"",
		[]query{
			{"http://co.uk", ""},
			{"http://uk", ""},
			{"http://some.co.uk", ""},
			{"http://foo.some.co.uk", ""},
			{"http://any.uk", ""},
		},
	},
}

func TestDomainHandling(t *testing.T) {
	for _, test := range domainHandlingTests {
		jar := newTestJar("")
		test.run(t, jar)
	}
}

type mergeCookie struct {
	when   time.Time
	url    string
	cookie string
}

func (c mergeCookie) set(jar *Jar) {
	setCookies(jar, c.url, []string{c.cookie}, c.when)
}

var mergeTests = []struct {
	description string
	setCookies0 []mergeCookie
	setCookies1 []mergeCookie
	now         time.Time
	content     string
	queries     []query // Queries to test the Jar.Cookies method
}{{
	description: "empty jar1",
	setCookies0: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; max-age=10"},
	},
	now:     atTime(1),
	content: "A=a",
}, {
	description: "empty jar0",
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; max-age=10"},
	},
	now:     atTime(1),
	content: "A=a",
}, {
	description: "simple override (1)",
	setCookies0: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=b; max-age=10"},
	},
	now:     atTime(2),
	content: "A=b",
}, {
	description: "simple override (2)",
	setCookies0: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=b; max-age=10"},
	},
	now:     atTime(2),
	content: "A=a",
}, {
	description: "expired cookie overrides unexpired cookie",
	setCookies0: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a; max-age=-1"},
	},
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=b; max-age=10"},
	},
	now:     atTime(2),
	content: "",
}, {
	description: "set overrides expires",
	setCookies0: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=b; max-age=-1"},
	},
	now:     atTime(2),
	content: "A=a",
}, {
	description: "expiry times preserved",
	setCookies0: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a; " + expiresIn(5)},
	},
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "B=b; " + expiresIn(4)},
	},
	now:     atTime(2),
	content: "A=a B=b",
	queries: []query{
		{"http://www.host.test", "B=b A=a"},
		{"http://www.host.test", "A=a"},
		{"http://www.host.test", ""},
	},
}, {
	description: "prefer receiver when creation times are identical",
	setCookies0: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=b; max-age=10"},
	},
	now:     atTime(2),
	content: "A=a",
}, {
	description: "max-age is persistent even when negative",
	setCookies0: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=b; max-age=-1"},
	},
	now:     atTime(2),
	content: "",
}, {
	description: "expires is persistent even when in the past",
	setCookies0: []mergeCookie{
		{atTime(0), "http://www.host.test", "A=a; " + expiresIn(2)},
	},
	setCookies1: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=b; " + expiresIn(-1)},
	},
	now:     atTime(2),
	content: "",
}, {
	description: "many hosts",
	setCookies0: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a0; max-age=10"},
		{atTime(2), "http://www.host.test/foo/", "A=foo0; max-age=10"},
		{atTime(1), "http://www.elsewhere", "X=x; max-age=10"},
	},
	setCookies1: []mergeCookie{
		{atTime(1), "http://www.host.test", "A=a1; max-age=10"},
		{atTime(3), "http://www.host.test", "B=b; max-age=10"},
		{atTime(1), "http://www.host.test/foo/", "A=foo1; max-age=10"},
		{atTime(0), "http://www.host.test/foo/", "C=arble; max-age=10"},
		{atTime(1), "http://nowhere.com", "A=n; max-age=10"},
	},
	now:     atTime(2),
	content: "A=a0 A=foo0 A=n B=b C=arble X=x",
	queries: []query{
		{"http://www.host.test/", "A=a0 B=b"},
		{"http://www.host.test/foo/", "C=arble A=foo0 A=a0 B=b"},
		{"http://nowhere.com", "A=n"},
		{"http://www.elsewhere", "X=x"},
	},
}}

func TestSaveMerge(t *testing.T) {
	dir, err := ioutil.TempDir("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for i, test := range mergeTests {
		path := filepath.Join(dir, fmt.Sprintf("jar%d", i))
		jar0 := newTestJar(path)
		for _, sc := range test.setCookies0 {
			sc.set(jar0)
		}
		jar1 := newTestJar(path)
		for _, sc := range test.setCookies1 {
			sc.set(jar1)
		}
		err := jar1.save(test.now)
		if err != nil {
			t.Fatalf("Test %q; cannot save first jar: %v", test.description, err)
		}
		err = jar0.save(test.now)
		if err != nil {
			t.Fatalf("Test %q; cannot save: %v", test.description, err)
		}
		got := allCookies(jar0, test.now)

		// Make sure jar content matches our expectations.
		if got != test.content {
			t.Logf("entries: %#v", jar0.entries)
			t.Errorf("Test %q Content\ngot  %q\nwant %q",
				test.description, got, test.content)
		}
		testQueries(t, test.queries, test.description, jar0, test.now)
	}
}

func TestMergeConcurrent(t *testing.T) {
	// This test is designed to fail when run with the race
	// detector. The actual final content of the jars is non-deterministic
	// so we don't test that.
	const N = 10

	f, err := ioutil.TempFile("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	jar0 := newTestJar(f.Name())
	jar1 := newTestJar(f.Name())
	var wg sync.WaitGroup
	url := mustParseURL("http://foo.com")
	merger := func(j *Jar) {
		defer wg.Done()
		for i := 0; i < N; i++ {
			j.Save()
		}
	}
	getter := func(j *Jar) {
		defer wg.Done()
		for i := 0; i < N; i++ {
			j.Cookies(url)
		}
	}
	setter := func(j *Jar, what string) {
		defer wg.Done()
		for i := 0; i < N; i++ {
			setCookies(j, url.String(), []string{fmt.Sprintf("A=a%s%d; max-age=10", what, i)}, time.Now())
		}
	}
	wg.Add(1)
	go merger(jar1)
	wg.Add(1)
	go merger(jar0)
	wg.Add(1)
	go getter(jar0)
	wg.Add(1)
	go getter(jar1)
	wg.Add(1)
	go setter(jar0, "first")
	wg.Add(1)
	go setter(jar1, "second")

	wg.Wait()
}

func TestDeleteExpired(t *testing.T) {
	expirySeconds := int(expiryRemovalDuration / time.Second)
	jar := newTestJar("")

	now := tNow
	setCookies(jar, "http://foo.com", []string{
		"a=a; " + expiresIn(1),
		"b=b; " + expiresIn(expirySeconds+3),
	}, tNow)
	setCookies(jar, "http://bar.com", []string{
		"c=c; " + expiresIn(1),
	}, tNow)
	// Make sure all the cookies are there to start with.
	got := allCookies(jar, now)
	want := "a=a b=b c=c"
	// Make sure jar content matches our expectations.
	if got != want {
		t.Errorf("Unexpected content\ngot  %q\nwant %q", got, want)
	}

	now = now.Add(expiryRemovalDuration - time.Millisecond)
	// Ensure that they've timed out but their entries
	// are still around before the cutoff period.
	jar.deleteExpired(now)
	got = allCookiesIncludingExpired(jar, now)
	want = "a= b=b c="
	if got != want {
		t.Errorf("Unexpected content\ngot  %q\nwant %q", got, want)
	}

	// Try just after the expiry duration. The entries should really have
	// been removed now.
	now = now.Add(2 * time.Millisecond)
	jar.deleteExpired(now)
	got = allCookiesIncludingExpired(jar, now)
	want = "b=b"
	if got != want {
		t.Errorf("Unexpected content\ngot  %q\nwant %q", got, want)
	}
}

var serializeTestCookies = []*http.Cookie{{
	Name:       "foo",
	Value:      "bar",
	Path:       "/p",
	Domain:     "example.com",
	Expires:    time.Now(),
	RawExpires: time.Now().Format(time.RFC3339Nano),
	MaxAge:     99,
	Secure:     true,
	HttpOnly:   true,
	Raw:        "raw string",
	Unparsed:   []string{"x", "y", "z"},
}}

var serializeTestURL, _ = url.Parse("http://example.com/x")

func TestLoadSave(t *testing.T) {
	c := qt.New(t)
	d, err := ioutil.TempDir("", "")
	c.Assert(err, qt.Equals, nil)
	defer os.RemoveAll(d)
	file := filepath.Join(d, "cookies")
	j := newTestJar(file)
	j.SetCookies(serializeTestURL, serializeTestCookies)
	err = j.Save()
	c.Assert(err, qt.Equals, nil)
	_, err = os.Stat(file)
	c.Assert(err, qt.Equals, nil)
	j1 := newTestJar(file)
	c.Assert(len(j1.entries), qt.Equals, len(serializeTestCookies))
	c.Assert(j1.entries, qt.DeepEquals, j.entries)
}

func TestMarshalJSON(t *testing.T) {
	c := qt.New(t)
	j := newTestJar("")
	j.SetCookies(serializeTestURL, serializeTestCookies)
	// Marshal the cookies.
	data, err := j.MarshalJSON()
	c.Assert(err, qt.Equals, nil)
	// Save them to disk.
	d, err := ioutil.TempDir("", "")
	c.Assert(err, qt.Equals, nil)
	defer os.RemoveAll(d)
	file := filepath.Join(d, "cookies")
	err = ioutil.WriteFile(file, data, 0600)
	c.Assert(err, qt.Equals, nil)
	// Load cookies from the file.
	j1 := newTestJar(file)
	c.Assert(len(j1.entries), qt.Equals, len(serializeTestCookies))
	c.Assert(j1.entries, qt.DeepEquals, j.entries)
}

func TestLoadSaveWithNoPersist(t *testing.T) {
	// Create a cookie file so that we can verify
	// that it's not read when NoPersist is set.
	d, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("cannot make temp dir: %v", err)
	}
	defer os.RemoveAll(d)
	file := filepath.Join(d, "cookies")
	j := newTestJar(file)
	j.SetCookies(serializeTestURL, serializeTestCookies)
	if err := j.Save(); err != nil {
		t.Fatalf("cannot save: %v", err)
	}
	jar, err := New(&Options{
		PublicSuffixList: testPSL{},
		Filename:         file,
		NoPersist:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := allCookiesIncludingExpired(jar, tNow); got != "" {
		t.Errorf("Cookies unexpectedly loaded: %v", got)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	if err := jar.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatalf("file was unexpectedly saved")
	}
}

func TestLoadNonExistentParent(t *testing.T) {
	d, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("cannot make temp dir: %v", err)
	}
	defer os.RemoveAll(d)
	file := filepath.Join(d, "foo", "cookies")
	_, err = New(&Options{
		PublicSuffixList: testPSL{},
		Filename:         file,
	})
	if err != nil {
		t.Fatalf("cannot make cookie jar: %v", err)
	}
}

func TestLoadNonExistentParentOfParent(t *testing.T) {
	d, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("cannot make temp dir: %v", err)
	}
	defer os.RemoveAll(d)
	file := filepath.Join(d, "foo", "foo", "cookies")
	_, err = New(&Options{
		PublicSuffixList: testPSL{},
		Filename:         file,
	})
	if err != nil {
		t.Fatalf("cannot make cookie jar: %v", err)
	}
}

func TestLoadOldFormat(t *testing.T) {
	// Check that loading the old format (a JSON object)
	// doesn't result in an error.
	f, err := ioutil.TempFile("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write([]byte("{}"))
	f.Close()
	jar, err := New(&Options{
		Filename: f.Name(),
	})
	if err != nil {
		t.Errorf("got error: %v", err)
	}
	if jar == nil {
		t.Errorf("nil jar")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	f, err := ioutil.TempFile("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write([]byte("["))
	f.Close()
	jar, err := New(&Options{
		Filename: f.Name(),
	})
	if err == nil {
		t.Fatalf("expected error, got none")
	}
	want := "cannot load cookies: unexpected EOF"
	if ok, _ := regexp.MatchString(want, err.Error()); !ok {
		t.Fatalf("unexpected error message; want %q got %q", want, err.Error())
	}
	if jar != nil {
		t.Fatalf("got nil jar")
	}
}

func TestLoadDifferentPublicSuffixList(t *testing.T) {
	f, err := ioutil.TempFile("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())
	now := tNow
	// With no public suffix list, some domains that should be
	// separate can set cookies for each other.
	jar, err := newAtTime(&Options{
		Filename:         f.Name(),
		PublicSuffixList: emptyPSL{},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	setCookies(jar, "http://foo.co.uk", []string{
		"a=a; max-age=10; domain=.co.uk",
	}, now)
	setCookies(jar, "http://bar.co.uk", []string{
		"b=b; max-age=10; domain=.co.uk",
	}, now)

	// With the default public suffix, the cookies are
	// correctly segmented into their proper domains.
	queries := []query{
		{"http://foo.co.uk/", "a=a b=b"},
		{"http://bar.co.uk/", "a=a b=b"},
	}
	testQueries(t, queries, "no public suffix list", jar, now)
	if err := jar.save(now); err != nil {
		t.Fatalf("cannot save jar: %v", err)
	}

	jar, err = newAtTime(&Options{
		Filename:         f.Name(),
		PublicSuffixList: testPSL{},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	queries = []query{
		{"http://foo.co.uk/", "a=a"},
		{"http://bar.co.uk/", "b=b"},
	}
	testQueries(t, queries, "with test public suffix list", jar, now)
	if err := jar.save(now); err != nil {
		t.Fatalf("cannot save jar: %v", err)
	}

	// When we reload with the original (empty) public suffix
	// we get all the original cookies back.
	jar, err = newAtTime(&Options{
		Filename:         f.Name(),
		PublicSuffixList: emptyPSL{},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	queries = []query{
		{"http://foo.co.uk/", "a=a b=b"},
		{"http://bar.co.uk/", "a=a b=b"},
	}
	testQueries(t, queries, "no public suffix list #2", jar, now)
	if err := jar.save(now); err != nil {
		t.Fatalf("cannot save jar: %v", err)
	}
}

func TestLockFile(t *testing.T) {
	d, err := ioutil.TempDir("", "cookiejar_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	filename := filepath.Join(d, "lockfile")
	concurrentCount := int64(0)
	var wg sync.WaitGroup
	locker := func() {
		defer wg.Done()
		closer, err := lockFile(filename)
		if err != nil {
			t.Errorf("cannot obtain lock: %v", err)
			return
		}
		x := atomic.AddInt64(&concurrentCount, 1)
		if x > 1 {
			t.Errorf("multiple locks held at one time")
		}
		defer closer.Close()
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt64(&concurrentCount, -1)
	}
	wg.Add(4)
	for i := 0; i < 4; i++ {
		go locker()
	}
	wg.Wait()
	if concurrentCount != 0 {
		t.Errorf("expected no running goroutines left")
	}
}

// jarTest encapsulates the following actions on a jar:
//   1. Perform SetCookies with fromURL and the cookies from setCookies.
//      (Done at time tNow + 0 ms.)
//   2. Check that the entries in the jar matches content.
//      (Done at time tNow + 1001 ms.)
//   3. For each query in tests: Check that Cookies with toURL yields the
//      cookies in want.
//      (Query n done at tNow + (n+2)*1001 ms.)
type jarTest struct {
	description string   // The description of what this test is supposed to test
	fromURL     string   // The full URL of the request from which Set-Cookie headers where received
	setCookies  []string // All the cookies received from fromURL
	content     string   // The whole (non-expired) content of the jar
	queries     []query  // Queries to test the Jar.Cookies method
}

// query contains one test of the cookies returned from Jar.Cookies.
type query struct {
	toURL string // the URL in the Cookies call
	want  string // the expected list of cookies (order matters)
}

// run runs the jarTest.
func (test jarTest) run(t *testing.T, jar *Jar) {
	now := tNow

	// Populate jar with cookies.
	setCookies(jar, test.fromURL, test.setCookies, now)
	now = now.Add(1001 * time.Millisecond)

	got := allCookies(jar, now)

	// Make sure jar content matches our expectations.
	if got != test.content {
		t.Errorf("Test %q Content\ngot  %q\nwant %q",
			test.description, got, test.content)
	}

	testQueries(t, test.queries, test.description, jar, now)
}

// setCookies sets the given cookies in the given jar associated
// with the given URL at the given time.
func setCookies(jar *Jar, fromURL string, cookies []string, now time.Time) {
	setCookies := make([]*http.Cookie, len(cookies))
	for i, cs := range cookies {
		cookies := (&http.Response{Header: http.Header{"Set-Cookie": {cs}}}).Cookies()
		if len(cookies) != 1 {
			panic(fmt.Sprintf("Wrong cookie line %q: %#v", cs, cookies))
		}
		setCookies[i] = cookies[0]
	}
	jar.setCookies(mustParseURL(fromURL), setCookies, now)
}

// allCookies returns all unexpired cookies in the jar
// in the form "name1=val1 name2=val2"
// (entries sorted by string).
func allCookies(jar *Jar, now time.Time) string {
	var cs []string
	for _, submap := range jar.entries {
		for _, cookie := range submap {
			if !cookie.Expires.After(now) {
				continue
			}
			cs = append(cs, cookie.Name+"="+cookie.Value)
		}
	}
	sort.Strings(cs)
	return strings.Join(cs, " ")
}

// allCookiesIncludingExpired returns all cookies in the jar
// in the form "name1=val1 name2=val2"
// (entries sorted by string), including cookies that
// have expired (without their values)
func allCookiesIncludingExpired(jar *Jar, now time.Time) string {
	var cs []string
	for _, submap := range jar.entries {
		for _, cookie := range submap {
			if !cookie.Expires.After(now) {
				cs = append(cs, cookie.Name+"=")
			} else {
				cs = append(cs, cookie.Name+"="+cookie.Value)
			}
		}
	}
	sort.Strings(cs)
	return strings.Join(cs, " ")
}

func testQueries(t *testing.T, queries []query, description string, jar *Jar, now time.Time) {
	// Test different calls to Cookies.
	for i, query := range queries {
		now = now.Add(1001 * time.Millisecond)
		if got := queryJar(jar, query.toURL, now); got != query.want {
			t.Errorf("Test %q #%d\ngot  %q\nwant %q", description, i, got, query.want)
		}
	}
}

// queryJar returns the results of querying jar for
// cookies associated with url at the given time,
// in "name1=val1 name2=val2" form.
func queryJar(jar *Jar, toURL string, now time.Time) string {
	var s []string
	for _, c := range jar.cookies(mustParseURL(toURL), now) {
		s = append(s, c.Name+"="+c.Value)
	}
	return strings.Join(s, " ")
}

// expiresIn creates an expires attribute delta seconds from tNow.
func expiresIn(delta int) string {
	return "expires=" + atTime(delta).Format(time.RFC1123)
}

// atTime returns a time delta seconds from tNow.
func atTime(delta int) time.Time {
	return tNow.Add(time.Duration(delta) * time.Second)
}

// mustParseURL parses s to an URL and panics on error.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		panic(fmt.Sprintf("Unable to parse URL %s.", s))
	}
	return u
}

type setCommand struct {
	url     *url.URL
	cookies []*http.Cookie
}

var allCookiesTests = []struct {
	about         string
	set           []setCommand
	expectCookies []*http.Cookie
}{{
	about: "no cookies",
}, {
	about: "a cookie",
	set: []setCommand{{
		url: mustParseURL("https://www.google.com/"),
		cookies: []*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: tNow.Add(24 * time.Hour),
			},
		},
	}},
	expectCookies: []*http.Cookie{
		&http.Cookie{
			Name:     "test-cookie",
			Value:    "test-value",
			Domain:   "www.google.com",
			Path:     "/",
			Secure:   false,
			HttpOnly: false,
			Expires:  tNow.Add(24 * time.Hour),
		},
	},
}, {
	about: "expired cookie",
	set: []setCommand{{
		url: mustParseURL("https://www.google.com/"),
		cookies: []*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: tNow.Add(-24 * time.Hour),
			},
		},
	}},
}, {
	about: "cookie for subpath",
	set: []setCommand{{
		url: mustParseURL("https://www.google.com/subpath/place"),
		cookies: []*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: tNow.Add(24 * time.Hour),
			},
		},
	}},
	expectCookies: []*http.Cookie{
		&http.Cookie{
			Name:     "test-cookie",
			Value:    "test-value",
			Domain:   "www.google.com",
			Path:     "/subpath",
			Secure:   false,
			HttpOnly: false,
			Expires:  tNow.Add(24 * time.Hour),
		},
	},
}, {
	about: "multiple cookies",
	set: []setCommand{{
		url: mustParseURL("https://www.google.com/"),
		cookies: []*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: tNow.Add(24 * time.Hour),
			},
		},
	}, {
		url: mustParseURL("https://www.google.com/subpath/"),
		cookies: []*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: tNow.Add(24 * time.Hour),
			},
		},
	}},
	expectCookies: []*http.Cookie{
		&http.Cookie{
			Name:     "test-cookie",
			Value:    "test-value",
			Domain:   "www.google.com",
			Path:     "/subpath",
			Secure:   false,
			HttpOnly: false,
			Expires:  tNow.Add(24 * time.Hour),
		},
		&http.Cookie{
			Name:     "test-cookie",
			Value:    "test-value",
			Domain:   "www.google.com",
			Path:     "/",
			Secure:   false,
			HttpOnly: false,
			Expires:  tNow.Add(24 * time.Hour),
		},
	},
}}

func TestAllCookies(t *testing.T) {
	dir, err := ioutil.TempDir("", "cookiejar-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for i, test := range allCookiesTests {
		path := filepath.Join(dir, fmt.Sprintf("jar%d", i))
		jar := newTestJar(path)
		for _, s := range test.set {
			jar.setCookies(s.url, s.cookies, tNow)
		}
		gotCookies := jar.allCookies(tNow)
		if len(gotCookies) != len(test.expectCookies) {
			t.Fatalf("Test %q: unexpected number of cookies returned, expected: %d, got: %d", test.about, len(test.expectCookies), len(gotCookies))
		}
		for j, c := range test.expectCookies {
			if !cookiesEqual(c, gotCookies[j]) {
				t.Fatalf("Test %q: mismatch in cookies[%d], expected: %#v, got: %#v", test.about, j, *c, *gotCookies[j])
			}
		}
	}
}

func TestRemoveCookies(t *testing.T) {
	jar := newTestJar("")
	jar.SetCookies(
		mustParseURL("https://www.google.com"),
		[]*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
			&http.Cookie{
				Name:    "test-cookie2",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
		},
	)
	cookies := jar.AllCookies()
	if len(cookies) != 2 {
		t.Fatalf("Expected 2 cookies got %d", len(cookies))
	}
	jar.RemoveCookie(cookies[0])
	cookies2 := jar.AllCookies()
	if len(cookies2) != 1 {
		t.Fatalf("Expected 1 cookie got %d", len(cookies))
	}
	if !cookiesEqual(cookies[1], cookies2[0]) {
		t.Fatalf("Unexpected cookie removed")
	}
}

func TestRemoveAllHost(t *testing.T) {
	testRemoveAllHost(t, mustParseURL("https://www.apple.com"), "www.apple.com", true)
}

func TestRemoveAllHostRoot(t *testing.T) {
	testRemoveAllHost(t, mustParseURL("https://www.apple.com"), "apple.com", false)
}

func TestRemoveAllHostDifferent(t *testing.T) {
	testRemoveAllHost(t, mustParseURL("https://www.apple.com"), "foo.apple.com", false)
}

func TestRemoveAllHostWithPort(t *testing.T) {
	testRemoveAllHost(t, mustParseURL("https://www.apple.com"), "www.apple.com:80", true)
}

func TestRemoveAllHostIP(t *testing.T) {
	testRemoveAllHost(t, mustParseURL("https://10.1.1.1"), "10.1.1.1", true)
}

func testRemoveAllHost(t *testing.T, setURL *url.URL, removeHost string, shouldRemove bool) {
	jar := newTestJar("")
	google := mustParseURL("https://www.google.com")
	jar.SetCookies(
		google,
		[]*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
			&http.Cookie{
				Name:    "test-cookie2",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
		},
	)
	onlyGoogle := jar.AllCookies()
	if len(onlyGoogle) != 2 {
		t.Fatalf("Expected 2 cookies, got %d", len(onlyGoogle))
	}

	jar.SetCookies(
		setURL,
		[]*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie3",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
			&http.Cookie{
				Name:    "test-cookie4",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
		},
	)
	withSet := jar.AllCookies()
	if len(withSet) != 4 {
		t.Fatalf("Expected 4 cookies, got %d", len(withSet))
	}
	jar.RemoveAllHost(removeHost)
	after := jar.AllCookies()
	if !shouldRemove {
		if len(after) != len(withSet) {
			t.Fatalf("Expected %d cookies, got %d", len(withSet), len(after))
		}
		return
	}
	if len(after) != len(onlyGoogle) {
		t.Fatalf("Expected %d cookies, got %d", len(onlyGoogle), len(after))
	}
	if !cookiesEqual(onlyGoogle[0], after[0]) {
		t.Fatalf("Expected %v, got %v", onlyGoogle[0], after[0])
	}
	if !cookiesEqual(onlyGoogle[1], after[1]) {
		t.Fatalf("Expected %v, got %v", onlyGoogle[1], after[1])
	}
}

func TestRemoveAll(t *testing.T) {
	jar := newTestJar("")
	jar.SetCookies(
		mustParseURL("https://www.google.com"),
		[]*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
			&http.Cookie{
				Name:    "test-cookie2",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
		},
	)
	jar.SetCookies(
		mustParseURL("https://foo.com"),
		[]*http.Cookie{
			&http.Cookie{
				Name:    "test-cookie3",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
			&http.Cookie{
				Name:    "test-cookie4",
				Value:   "test-value",
				Expires: time.Now().Add(24 * time.Hour),
			},
		},
	)
	jar.RemoveAll()
	if after := len(jar.AllCookies()); after != 0 {
		t.Fatalf("%d cookies remaining after RemoveAll", after)
	}
}

func cookiesEqual(a, b *http.Cookie) bool {
	return a.Name == b.Name &&
		a.Value == b.Value &&
		a.Domain == b.Domain &&
		a.Path == b.Path &&
		a.Expires.Equal(b.Expires) &&
		a.HttpOnly == b.HttpOnly &&
		a.Secure == b.Secure
}
