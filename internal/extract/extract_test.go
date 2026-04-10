package extract

import (
	"reflect"
	"testing"
)

const productHTML = `
<html><body>
  <h1 class="title">  Widget  </h1>
  <span class="price">$19.99</span>
  <a href="/p/1" class="link">one</a>
  <a href="/p/2" class="link">two</a>
  <img src="/a.jpg" class="pic" />
  <img src="/b.jpg" class="pic" />
  <meta name="sku" content="ABC-123" />
</body></html>
`

func TestCSS(t *testing.T) {
	fields := []Field{
		{Name: "title", Selector: "h1.title"},
		{Name: "price", Selector: ".price"},
		{Name: "links", Selector: "a.link", Attr: "href", Multiple: true},
		{Name: "images", Selector: "img.pic", Attr: "src", Multiple: true},
		{Name: "sku", Selector: `meta[name="sku"]`, Attr: "content"},
		{Name: "missing", Selector: ".nope"},
	}

	got, err := CSS([]byte(productHTML), fields)
	if err != nil {
		t.Fatal(err)
	}

	if got["title"] != "Widget" {
		t.Errorf("title = %q", got["title"])
	}
	if got["price"] != "$19.99" {
		t.Errorf("price = %q", got["price"])
	}
	if got["sku"] != "ABC-123" {
		t.Errorf("sku = %q", got["sku"])
	}
	wantLinks := []string{"/p/1", "/p/2"}
	if !reflect.DeepEqual(got["links"], wantLinks) {
		t.Errorf("links = %v, want %v", got["links"], wantLinks)
	}
	wantImgs := []string{"/a.jpg", "/b.jpg"}
	if !reflect.DeepEqual(got["images"], wantImgs) {
		t.Errorf("images = %v, want %v", got["images"], wantImgs)
	}
	if _, exists := got["missing"]; exists {
		t.Errorf("missing field should be omitted, got %v", got["missing"])
	}
}

func TestParseFieldSpec(t *testing.T) {
	cases := []struct {
		in   string
		want Field
	}{
		{"title=h1.title", Field{Name: "title", Selector: "h1.title"}},
		{"price= .price ", Field{Name: "price", Selector: ".price"}},
		{"links=a.link[]", Field{Name: "links", Selector: "a.link", Multiple: true}},
		{"images=img.pic[]@src", Field{Name: "images", Selector: "img.pic", Attr: "src", Multiple: true}},
		{"sku=meta[name=sku]@content", Field{Name: "sku", Selector: "meta[name=sku]", Attr: "content"}},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseFieldSpec(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseFieldSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}

	badCases := []string{"", "noequals", "=selector", "name=", "="}
	for _, in := range badCases {
		t.Run("bad:"+in, func(t *testing.T) {
			if _, err := ParseFieldSpec(in); err == nil {
				t.Errorf("ParseFieldSpec(%q) expected error", in)
			}
		})
	}
}
