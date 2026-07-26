package s3compat

import "encoding/xml"

// ── Website Configuration ─────────────────────────────────────────────────────

type websiteConfiguration struct {
	XMLName       xml.Name         `xml:"WebsiteConfiguration"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	IndexDocument *websiteIndexDoc `xml:"IndexDocument,omitempty"`
	ErrorDocument *websiteErrorDoc `xml:"ErrorDocument,omitempty"`
}

type websiteIndexDoc struct {
	Suffix string `xml:"Suffix"`
}

type websiteErrorDoc struct {
	Key string `xml:"Key"`
}

// ── SSE Encryption ────────────────────────────────────────────────────────────

type serverSideEncryptionConfiguration struct {
	XMLName xml.Name                   `xml:"ServerSideEncryptionConfiguration"`
	XMLNS   string                     `xml:"xmlns,attr,omitempty"`
	Rules   []serverSideEncryptionRule `xml:"Rule"`
}

type serverSideEncryptionRule struct {
	Apply serverSideEncryptionApply `xml:"ApplyServerSideEncryptionByDefault"`
}

type serverSideEncryptionApply struct {
	SSEAlgorithm   string `xml:"SSEAlgorithm"`
	KMSMasterKeyID string `xml:"KMSMasterKeyID,omitempty"`
}

// ── Object Legal Hold & Retention ─────────────────────────────────────────────

type objectLegalHold struct {
	XMLName xml.Name `xml:"LegalHold"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Status  string   `xml:"Status"`
}

type objectRetention struct {
	XMLName         xml.Name `xml:"Retention"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	Mode            string   `xml:"Mode"`
	RetainUntilDate string   `xml:"RetainUntilDate"`
}
