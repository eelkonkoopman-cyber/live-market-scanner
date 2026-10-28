package edgar

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// primaryDoc is the structured Schedule 13G/13G-A XML every filer has shipped
// since 2023 (schema http://www.sec.gov/edgar/schedule13g) — confirmed
// against two independently captured real filings (an original and an
// amendment). Field tags match by LOCAL NAME only; the real documents use
// different namespace prefixes for unrelated fields ("ns1:" vs "com:" for
// the address block in the two captures), which Go's encoding/xml already
// ignores by default — verified, not assumed, in filing_test.go.
type primaryDoc struct {
	FormData struct {
		CoverPageHeader struct {
			IssuerInfo struct {
				IssuerName string `xml:"issuerName"`
			} `xml:"issuerInfo"`
		} `xml:"coverPageHeader"`
		ReportingPersonDetails struct {
			ReportingPersonName   string `xml:"reportingPersonName"`
			ClassPercent          string `xml:"classPercent"`
			TypeOfReportingPerson string `xml:"typeOfReportingPerson"`
		} `xml:"coverPageHeaderReportingPersonDetails"`
	} `xml:"formData"`
}

type dirListing struct {
	Directory struct {
		Item []struct {
			Name string `json:"name"`
		} `json:"item"`
	} `json:"directory"`
}

// buildItem turns one detected filing into a NewsItem + body, fetching the
// filing's own primary_doc.xml for the ownership percentage. ok=false means
// "skip this one" (unknown ticker, fetch failure, unparseable document) —
// none of these are fatal to the watcher; the next poll tick carries on.
func (w *Watcher) buildItem(ctx context.Context, e subjectEntry) (core.NewsItem, string, bool) {
	w.ensureTickersFresh(ctx)
	ticker, ok := w.tickers.lookup(e.IssuerCIK)
	if !ok {
		return core.NewsItem{}, "", false // not a ticker this app tracks (or not yet in the daily list)
	}

	doc, ok := w.fetchPrimaryDoc(ctx, e)
	if !ok {
		return core.NewsItem{}, "", false
	}

	form := "Schedule 13G"
	if e.Amendment {
		form = "Schedule 13G/A"
	}
	pct := strings.TrimSpace(doc.FormData.ReportingPersonDetails.ClassPercent)
	filer := strings.TrimSpace(doc.FormData.ReportingPersonDetails.ReportingPersonName)
	if filer == "" {
		filer = "An investor"
	}
	issuer := strings.TrimSpace(doc.FormData.CoverPageHeader.IssuerInfo.IssuerName)
	if issuer == "" {
		issuer = e.IssuerName
	}

	var headline string
	if pct != "" {
		headline = fmt.Sprintf("%s discloses %s%% stake in %s (%s)", filer, pct, issuer, form)
	} else {
		headline = fmt.Sprintf("%s files %s on %s", filer, form, issuer)
	}

	id := "sec13g" + strings.ReplaceAll(e.Accession, "-", "")
	body := fmt.Sprintf(
		"SEC %s filing.\nIssuer: %s\nFiling person: %s\nOwnership: %s%% of class\nType of reporting person: %s\n",
		form, issuer, filer, valueOr(pct, "n/a"), valueOr(doc.FormData.ReportingPersonDetails.TypeOfReportingPerson, "n/a"))

	ts := e.Filed.UnixMilli()
	if ts <= 0 {
		ts = timeNowMs()
	}
	item := core.NewsItem{
		ID:       id,
		Tickers:  []string{ticker},
		Headline: headline,
		Source:   "SEC EDGAR",
		URL:      e.IndexURL,
		TS:       ts,
		Teaser:   headline,
		HasBody:  true,
	}
	return item, body, true
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// fetchPrimaryDoc resolves and parses a filing's structured XML. The
// directory contents (index.json) are fetched first because the filename
// itself is always "primary_doc.xml" for this schema, but resolving it via
// the real listing — rather than assuming the URL — means a future filer-
// agent quirk that names it differently fails closed (skip) instead of
// silently reading nothing.
func (w *Watcher) fetchPrimaryDoc(ctx context.Context, e subjectEntry) (primaryDoc, bool) {
	accNoDash := strings.ReplaceAll(e.Accession, "-", "")
	dirURL := fmt.Sprintf("%s/%d/%s/index.json", w.archivesBase, e.IssuerCIK, accNoDash)
	body, err := w.getSEC(ctx, dirURL)
	if err != nil {
		logx.L().Warn("edgar13g: filing directory fetch failed", "accession", e.Accession, "err", err)
		return primaryDoc{}, false
	}
	var listing dirListing
	if err := json.Unmarshal(body, &listing); err != nil {
		logx.L().Warn("edgar13g: filing directory decode failed", "accession", e.Accession, "err", err)
		return primaryDoc{}, false
	}
	docName := ""
	for _, it := range listing.Directory.Item {
		if it.Name == "primary_doc.xml" {
			docName = it.Name
			break
		}
	}
	if docName == "" {
		return primaryDoc{}, false // pre-2023-style filing with no structured doc; out of scope
	}
	docURL := fmt.Sprintf("%s/%d/%s/%s", w.archivesBase, e.IssuerCIK, accNoDash, docName)
	xmlBody, err := w.getSEC(ctx, docURL)
	if err != nil {
		logx.L().Warn("edgar13g: primary_doc fetch failed", "accession", e.Accession, "err", err)
		return primaryDoc{}, false
	}
	doc, ok := parsePrimaryDoc(xmlBody)
	if !ok {
		logx.L().Warn("edgar13g: primary_doc decode failed", "accession", e.Accession)
	}
	return doc, ok
}

func parsePrimaryDoc(body []byte) (primaryDoc, bool) {
	var doc primaryDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return primaryDoc{}, false
	}
	if strings.TrimSpace(doc.FormData.CoverPageHeader.IssuerInfo.IssuerName) == "" {
		return primaryDoc{}, false
	}
	return doc, true
}

// timeNowMs is a var, not a direct time.Now() call, so tests can pin it —
// same pattern internal/schedule uses for its clock.
var timeNowMs = func() int64 { return time.Now().UnixMilli() }
