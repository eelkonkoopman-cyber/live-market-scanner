package edgar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realPrimaryDocOriginal is the ACTUAL primary_doc.xml of a real Schedule 13G
// filed 2026-07-24 (Davenport & Co LLC disclosing a 5.05% stake in Cable One,
// Inc.), captured live via curl with the fair-access UA — not hand-built.
// Namespace prefix for the address block: "ns1:".
const realPrimaryDocOriginal = `<?xml version="1.0" encoding="UTF-8"?><edgarSubmission xmlns="http://www.sec.gov/edgar/schedule13g" xmlns:ns1="http://www.sec.gov/edgar/common">
  <schemaVersion>X0202</schemaVersion>
<headerData>
    <submissionType>SCHEDULE 13G</submissionType>
    <filerInfo>
      <filer>
        <filerCredentials>
          <cik>0000887777</cik>
          <ccc>XXXXXXXX</ccc>
        </filerCredentials>
      </filer>
      <liveTestFlag>LIVE</liveTestFlag>
    </filerInfo>
  </headerData>
  <formData>
    <coverPageHeader>
      <securitiesClassTitle>COMMON STOCK</securitiesClassTitle>
      <eventDateRequiresFilingThisStatement>07/01/2026</eventDateRequiresFilingThisStatement>
      <issuerInfo>
        <issuerCik>0001632127</issuerCik>
        <issuerName>Cable One, Inc.</issuerName>
        <issuerCusips>
          <issuerCusipNumber>12685J105</issuerCusipNumber>
        </issuerCusips>
        <issuerPrincipalExecutiveOfficeAddress>
          <ns1:street1>210 E. EARLL DRIVE</ns1:street1>
          <ns1:city>PHOENIX</ns1:city>
          <ns1:stateOrCountry>AZ</ns1:stateOrCountry>
          <ns1:zipCode>85012</ns1:zipCode>
        </issuerPrincipalExecutiveOfficeAddress>
      </issuerInfo>
    </coverPageHeader>
    <coverPageHeaderReportingPersonDetails>
      <reportingPersonName>DAVENPORT &amp; Co LLC</reportingPersonName>
      <citizenshipOrOrganization>VA</citizenshipOrOrganization>
      <reportingPersonBeneficiallyOwnedAggregateNumberOfShares>286488</reportingPersonBeneficiallyOwnedAggregateNumberOfShares>
      <classPercent>5.05</classPercent>
      <typeOfReportingPerson>IA</typeOfReportingPerson>
    </coverPageHeaderReportingPersonDetails>
    <items>
      <item4>
        <amountBeneficiallyOwned>286,488</amountBeneficiallyOwned>
        <classPercent>5.05</classPercent>
      </item4>
    </items>
  </formData>
</edgarSubmission>`

// realPrimaryDocAmendment is a REAL Schedule 13G/A filed 2026-07-24 (Baillie
// Gifford & Co, amendment #1, reducing to 2.3% of Atlassian Corp) — captured
// the same way. Namespace prefix for the SAME address block: "com:", not
// "ns1:" — a filer-agent-dependent difference that must not matter, since
// field matching goes by local element name only.
const realPrimaryDocAmendment = `<?xml version="1.0" encoding="UTF-8"?><edgarSubmission xmlns="http://www.sec.gov/edgar/schedule13g" xmlns:com="http://www.sec.gov/edgar/common">
  <schemaVersion>X0202</schemaVersion>
<headerData>
    <submissionType>SCHEDULE 13G/A</submissionType>
    <previousAccessionNumber>0001088875-26-000006</previousAccessionNumber>
    <filerInfo>
      <filer>
        <filerCredentials>
          <cik>0001088875</cik>
          <ccc>XXXXXXXX</ccc>
        </filerCredentials>
      </filer>
      <liveTestFlag>LIVE</liveTestFlag>
    </filerInfo>
  </headerData>
  <formData>
    <coverPageHeader>
      <amendmentNo>1</amendmentNo>
      <securitiesClassTitle>Class A Common Stock</securitiesClassTitle>
      <eventDateRequiresFilingThisStatement>06/30/2026</eventDateRequiresFilingThisStatement>
      <issuerInfo>
        <issuerCik>0001650372</issuerCik>
        <issuerName>Atlassian Corp</issuerName>
        <issuerCusips>
          <issuerCusipNumber>049468101</issuerCusipNumber>
        </issuerCusips>
        <issuerPrincipalExecutiveOfficeAddress>
          <com:street1>350 BUSH STREET</com:street1>
          <com:city>SAN FRANCISCO</com:city>
          <com:stateOrCountry>CA</com:stateOrCountry>
          <com:zipCode>94104</com:zipCode>
        </issuerPrincipalExecutiveOfficeAddress>
      </issuerInfo>
    </coverPageHeader>
    <coverPageHeaderReportingPersonDetails>
      <reportingPersonName>BAILLIE GIFFORD &amp; CO</reportingPersonName>
      <citizenshipOrOrganization>X0</citizenshipOrOrganization>
      <reportingPersonBeneficiallyOwnedAggregateNumberOfShares>3658205.00</reportingPersonBeneficiallyOwnedAggregateNumberOfShares>
      <classPercent>2.3</classPercent>
      <typeOfReportingPerson>IA</typeOfReportingPerson>
    </coverPageHeaderReportingPersonDetails>
    <items>
      <item5>
        <classOwnership5PercentOrLess>Y</classOwnership5PercentOrLess>
      </item5>
    </items>
  </formData>
</edgarSubmission>`

func TestParsePrimaryDocOriginal(t *testing.T) {
	doc, ok := parsePrimaryDoc([]byte(realPrimaryDocOriginal))
	if !ok {
		t.Fatal("real captured 13G rejected")
	}
	if doc.FormData.CoverPageHeader.IssuerInfo.IssuerName != "Cable One, Inc." {
		t.Fatalf("issuer: %q", doc.FormData.CoverPageHeader.IssuerInfo.IssuerName)
	}
	if doc.FormData.ReportingPersonDetails.ReportingPersonName != "DAVENPORT & Co LLC" {
		t.Fatalf("filer (entity-decoded): %q", doc.FormData.ReportingPersonDetails.ReportingPersonName)
	}
	if doc.FormData.ReportingPersonDetails.ClassPercent != "5.05" {
		t.Fatalf("classPercent: %q", doc.FormData.ReportingPersonDetails.ClassPercent)
	}
	if doc.FormData.ReportingPersonDetails.TypeOfReportingPerson != "IA" {
		t.Fatalf("typeOfReportingPerson: %q", doc.FormData.ReportingPersonDetails.TypeOfReportingPerson)
	}
}

// The amendment uses a DIFFERENT XML namespace prefix ("com:" vs "ns1:") on
// an unrelated sibling element (the address block) — this proves field
// matching is by local name only, not accidentally dependent on whichever
// prefix a given filer's software happens to emit.
func TestParsePrimaryDocAmendmentDifferentNamespacePrefix(t *testing.T) {
	doc, ok := parsePrimaryDoc([]byte(realPrimaryDocAmendment))
	if !ok {
		t.Fatal("real captured 13G/A rejected")
	}
	if doc.FormData.CoverPageHeader.IssuerInfo.IssuerName != "Atlassian Corp" {
		t.Fatalf("issuer: %q", doc.FormData.CoverPageHeader.IssuerInfo.IssuerName)
	}
	if doc.FormData.ReportingPersonDetails.ReportingPersonName != "BAILLIE GIFFORD & CO" {
		t.Fatalf("filer: %q", doc.FormData.ReportingPersonDetails.ReportingPersonName)
	}
	if doc.FormData.ReportingPersonDetails.ClassPercent != "2.3" {
		t.Fatalf("classPercent: %q", doc.FormData.ReportingPersonDetails.ClassPercent)
	}
}

func TestParsePrimaryDocRejectsGarbage(t *testing.T) {
	if _, ok := parsePrimaryDoc([]byte(`<html><body>not a filing</body></html>`)); ok {
		t.Fatal("non-filing content must not parse as a valid document")
	}
	if _, ok := parsePrimaryDoc([]byte(`not even xml`)); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestBuildItemHeadlineAndBody(t *testing.T) {
	w := New()
	w.tickers.byCIK = map[int]string{1632127: "CABO"}

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "primary_doc.xml"):
			_, _ = rw.Write([]byte(realPrimaryDocOriginal))
		case strings.HasSuffix(r.URL.Path, "index.json"):
			_, _ = rw.Write([]byte(`{"directory":{"item":[{"name":"primary_doc.xml"}]}}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()
	w.archivesBase = srv.URL

	e := subjectEntry{
		IssuerName: "Cable One, Inc.",
		IssuerCIK:  1632127,
		Accession:  "0000887777-26-000022",
		IndexURL:   "https://www.sec.gov/Archives/edgar/data/1632127/000088777726000022/0000887777-26-000022-index.htm",
	}
	it, body, ok := w.buildItem(context.Background(), e)
	if !ok {
		t.Fatal("buildItem rejected a well-formed filing + known ticker")
	}
	if it.ID != "sec13g000088777726000022" {
		t.Fatalf("ID: %q", it.ID)
	}
	if len(it.Tickers) != 1 || it.Tickers[0] != "CABO" {
		t.Fatalf("Tickers: %+v", it.Tickers)
	}
	if it.Headline != "DAVENPORT & Co LLC discloses 5.05% stake in Cable One, Inc. (Schedule 13G)" {
		t.Fatalf("Headline: %q", it.Headline)
	}
	if it.Source != "SEC EDGAR" {
		t.Fatalf("Source: %q", it.Source)
	}
	if !strings.Contains(body, "5.05%") || !strings.Contains(body, "Cable One, Inc.") {
		t.Fatalf("body missing key facts: %q", body)
	}
}

func TestBuildItemSkipsUnknownTicker(t *testing.T) {
	w := New() // tickers cache empty -> every CIK is unknown
	e := subjectEntry{IssuerName: "Nobody Corp", IssuerCIK: 999999, Accession: "0000000000-26-000001"}
	_, _, ok := w.buildItem(context.Background(), e)
	if ok {
		t.Fatal("an issuer with no known ticker must be skipped, not fabricated")
	}
}
