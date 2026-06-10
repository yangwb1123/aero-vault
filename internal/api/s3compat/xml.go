package s3compat

import (
	"encoding/xml"
	"time"
)

const s3Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

type listBucketResult struct {
	XMLName               xml.Name      `xml:"ListBucketResult"`
	Xmlns                 string        `xml:"xmlns,attr"`
	Name                  string        `xml:"Name"`
	Prefix                string        `xml:"Prefix"`
	KeyCount              int           `xml:"KeyCount"`
	MaxKeys               int           `xml:"MaxKeys"`
	IsTruncated           bool          `xml:"IsTruncated"`
	ContinuationToken     string        `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string        `xml:"NextContinuationToken,omitempty"`
	StartAfter            string        `xml:"StartAfter,omitempty"`
	Contents              []listContent `xml:"Contents"`
}

// listBucketResultV1 is the ListObjects (v1) response: it echoes the request
// Marker and emits NextMarker when truncated, and never carries the v2-only
// KeyCount / ContinuationToken / NextContinuationToken fields.
type listBucketResultV1 struct {
	XMLName     xml.Name      `xml:"ListBucketResult"`
	Xmlns       string        `xml:"xmlns,attr"`
	Name        string        `xml:"Name"`
	Prefix      string        `xml:"Prefix"`
	Marker      string        `xml:"Marker"`
	NextMarker  string        `xml:"NextMarker,omitempty"`
	MaxKeys     int           `xml:"MaxKeys"`
	IsTruncated bool          `xml:"IsTruncated"`
	Contents    []listContent `xml:"Contents"`
}

type listContent struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
}

type s3Error struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId,omitempty"`
}

// --- CopyObject ---

type copyObjectResult struct {
	XMLName      xml.Name  `xml:"CopyObjectResult"`
	Xmlns        string    `xml:"xmlns,attr"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
}

// --- Tagging (GET/PUT ?tagging) ---

type s3Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type tagging struct {
	XMLName xml.Name `xml:"Tagging"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	TagSet  []s3Tag  `xml:"TagSet>Tag"`
}

// --- Multipart ---

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// completeMultipartUpload is the client-supplied part manifest. We persist parts
// server-side, so this is parsed for validation/compat but the stored parts are
// authoritative.
type completeMultipartUpload struct {
	XMLName xml.Name           `xml:"CompleteMultipartUpload"`
	Parts   []completePartItem `xml:"Part"`
}

type completePartItem struct {
	PartNumber int32  `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type listPartsResult struct {
	XMLName      xml.Name       `xml:"ListPartsResult"`
	Xmlns        string         `xml:"xmlns,attr"`
	Bucket       string         `xml:"Bucket"`
	Key          string         `xml:"Key"`
	UploadID     string         `xml:"UploadId"`
	StorageClass string         `xml:"StorageClass"`
	IsTruncated  bool           `xml:"IsTruncated"`
	Parts        []listPartItem `xml:"Part"`
}

type listPartItem struct {
	PartNumber int32  `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
	Size       int64  `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName xml.Name         `xml:"ListMultipartUploadsResult"`
	Xmlns   string           `xml:"xmlns,attr"`
	Bucket  string           `xml:"Bucket"`
	Prefix  string           `xml:"Prefix"`
	Uploads []uploadListItem `xml:"Upload"`
}

type uploadListItem struct {
	Key       string    `xml:"Key"`
	UploadID  string    `xml:"UploadId"`
	Initiated time.Time `xml:"Initiated"`
}

// --- Batch delete (POST ?delete) ---

type deleteRequest struct {
	XMLName xml.Name              `xml:"Delete"`
	Quiet   bool                  `xml:"Quiet"`
	Objects []deleteRequestObject `xml:"Object"`
}

type deleteRequestObject struct {
	Key string `xml:"Key"`
}

type deleteResult struct {
	XMLName xml.Name        `xml:"DeleteResult"`
	Xmlns   string          `xml:"xmlns,attr"`
	Deleted []deletedItem   `xml:"Deleted"`
	Errors  []deleteErrItem `xml:"Error"`
}

type deletedItem struct {
	Key string `xml:"Key"`
}

type deleteErrItem struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// --- ACL (GET ?acl) ---

type accessControlPolicy struct {
	XMLName xml.Name   `xml:"AccessControlPolicy"`
	Xmlns   string     `xml:"xmlns,attr"`
	Owner   aclOwner   `xml:"Owner"`
	Grants  []aclGrant `xml:"AccessControlList>Grant"`
}

type aclOwner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type aclGrant struct {
	Grantee    aclGrantee `xml:"Grantee"`
	Permission string     `xml:"Permission"`
}

type aclGrantee struct {
	Type string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	ID   string `xml:"ID,omitempty"`
	URI  string `xml:"URI,omitempty"`
}

// --- Bucket sub-resources (versioning / lifecycle / object-lock / versions) ---

// versioningConfiguration is the body of GET/PUT /{bucket}?versioning. AWS omits
// <Status> when versioning was never configured, so Status uses omitempty.
type versioningConfiguration struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Status  string   `xml:"Status,omitempty"`
}

// lifecycleConfiguration is the body of GET/PUT /{bucket}?lifecycle. We model the
// single expire-after-days rule the service supports.
type lifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Xmlns   string          `xml:"xmlns,attr,omitempty"`
	Rules   []lifecycleRule `xml:"Rule"`
}

type lifecycleRule struct {
	ID         string               `xml:"ID,omitempty"`
	Status     string               `xml:"Status"`
	Expiration *lifecycleExpiration `xml:"Expiration,omitempty"`
}

type lifecycleExpiration struct {
	Days int `xml:"Days"`
}

// objectLockConfiguration is the body of GET/PUT /{bucket}?object-lock.
type objectLockConfiguration struct {
	XMLName           xml.Name        `xml:"ObjectLockConfiguration"`
	Xmlns             string          `xml:"xmlns,attr,omitempty"`
	ObjectLockEnabled string          `xml:"ObjectLockEnabled"`
	Rule              *objectLockRule `xml:"Rule,omitempty"`
}

type objectLockRule struct {
	DefaultRetention objectLockRetention `xml:"DefaultRetention"`
}

type objectLockRetention struct {
	Mode string `xml:"Mode"`
	Days int    `xml:"Days,omitempty"`
}

// listVersionsResult is the body of GET /{bucket}?versions.
type listVersionsResult struct {
	XMLName       xml.Name       `xml:"ListVersionsResult"`
	Xmlns         string         `xml:"xmlns,attr"`
	Name          string         `xml:"Name"`
	Prefix        string         `xml:"Prefix"`
	KeyMarker     string         `xml:"KeyMarker"`
	NextKeyMarker string         `xml:"NextKeyMarker,omitempty"`
	MaxKeys       int            `xml:"MaxKeys"`
	IsTruncated   bool           `xml:"IsTruncated"`
	Versions      []versionEntry `xml:"Version"`
}

type versionEntry struct {
	Key          string    `xml:"Key"`
	VersionID    string    `xml:"VersionId"`
	IsLatest     bool      `xml:"IsLatest"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
}

const allUsersURI = "http://acs.amazonaws.com/groups/global/AllUsers"

// cannedToPolicy synthesizes an AccessControlPolicy from a canned ACL.
func cannedToPolicy(acl string) accessControlPolicy {
	p := accessControlPolicy{
		Xmlns: s3Namespace,
		Owner: aclOwner{ID: "aero-vault", DisplayName: "aero-vault"},
		Grants: []aclGrant{
			{Grantee: aclGrantee{Type: "CanonicalUser", ID: "aero-vault"}, Permission: "FULL_CONTROL"},
		},
	}
	switch acl {
	case "public-read":
		p.Grants = append(p.Grants, aclGrant{Grantee: aclGrantee{Type: "Group", URI: allUsersURI}, Permission: "READ"})
	case "public-read-write":
		p.Grants = append(p.Grants,
			aclGrant{Grantee: aclGrantee{Type: "Group", URI: allUsersURI}, Permission: "READ"},
			aclGrant{Grantee: aclGrantee{Type: "Group", URI: allUsersURI}, Permission: "WRITE"})
	case "authenticated-read":
		p.Grants = append(p.Grants, aclGrant{Grantee: aclGrantee{Type: "Group", URI: authUsersURI}, Permission: "READ"})
	}
	return p
}

const authUsersURI = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"

// policyToCanned maps an AccessControlPolicy body back to the nearest canned
// ACL aero-vault stores: AllUsers READ+WRITE → public-read-write, AllUsers READ
// → public-read, AuthenticatedUsers READ → authenticated-read, else private.
func policyToCanned(p accessControlPolicy) string {
	allUsersRead, allUsersWrite, authRead := false, false, false
	for _, g := range p.Grants {
		switch g.Grantee.URI {
		case allUsersURI:
			switch g.Permission {
			case "READ":
				allUsersRead = true
			case "WRITE":
				allUsersWrite = true
			case "FULL_CONTROL":
				allUsersRead, allUsersWrite = true, true
			}
		case authUsersURI:
			if g.Permission == "READ" || g.Permission == "FULL_CONTROL" {
				authRead = true
			}
		}
	}
	switch {
	case allUsersRead && allUsersWrite:
		return "public-read-write"
	case allUsersRead:
		return "public-read"
	case authRead:
		return "authenticated-read"
	default:
		return "private"
	}
}

// locationConstraint is the body of GET /{bucket}?location. aero-vault is
// single-region; an empty constraint is what S3 clients read as us-east-1.
type locationConstraint struct {
	XMLName  xml.Name `xml:"LocationConstraint"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:",chardata"`
}
