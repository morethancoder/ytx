package analyze

import (
	"reflect"
	"testing"

	"github.com/morethancoder/ytx/internal/model"
)

func TestMedianOddEvenSkipsZeroAndLive(t *testing.T) {
	odd := []model.Video{{Views: 10}, {Views: 30}, {Views: 20}}
	if got := Median(odd); got != 20 {
		t.Fatalf("odd median = %d", got)
	}
	even := []model.Video{{Views: 10}, {Views: 20}, {Views: 30}, {Views: 50}, {Views: 0}, {Views: 999, IsLive: true}}
	if got := Median(even); got != 25 {
		t.Fatalf("even median = %d", got)
	}
	if Median(nil) != 0 {
		t.Fatal("empty median not 0")
	}
}

func TestOutlier(t *testing.T) {
	if Outlier(300, 100) != 3 || Outlier(150, 100) != 1.5 || Outlier(5, 0) != 0 {
		t.Fatal("outlier maths")
	}
}

func TestTermSetMissing(t *testing.T) {
	cov := TermSet("We install the agents and configure hooks")
	terms := Terms("How do I configure agent hooks with docker?")
	miss := Missing(terms, cov)
	if !reflect.DeepEqual(miss, []string{"docker"}) {
		t.Fatalf("missing = %v", miss)
	}
	if MostlyMissing(terms, miss) {
		t.Fatal("1 of 3 is not most")
	}
	if !MostlyMissing([]string{"a1b", "c2d"}, []string{"a1b", "c2d"}) || MostlyMissing(nil, nil) {
		t.Fatal("MostlyMissing")
	}
}

func TestTermsArabic(t *testing.T) {
	got := Terms("كيف أتعلم البرمجة")
	if len(got) == 0 {
		t.Fatal("arabic terms dropped")
	}
}
