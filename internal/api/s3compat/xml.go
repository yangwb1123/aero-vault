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
	XMLName              xml.Name       `xml:"ListPartsResult"`
	Xmlns                string         `xml:"xmlns,attr"`
	Bucket               string         `xml:"Bucket"`
	Key                  string         `xml:"Key"`
	UploadID             string         `xml:"UploadId"`
	StorageClass         string         `xml:"StorageClass"`
	PartNumberMarker     int32          `xml:"PartNumberMarker"`
	NextPartNumberMarker int32          `xml:"NextPartNumberMarker"`
	MaxParts             int            `xml:"MaxParts"`
	IsTruncated          bool           `xml:"IsTruncated"`
	Parts                []listPartItem `xml:"Part"`
}

type listPartItem struct {
	PartNumber int32  `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
	Size       int64  `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName            xml.Name         `xml:"ListMultipartUploadsResult"`
	Xmlns              string           `xml:"xmlns,attr"`
	Bucket             string           `xml:"Bucket"`
	Prefix             string           `xml:"Prefix"`
	KeyMarker          string           `xml:"KeyMarker"`
	UploadIDMarker     string           `xml:"UploadIdMarker"`
	NextKeyMarker      string           `xml:"NextKeyMarker"`
	NextUploadIDMarker string           `xml:"NextUploadIdMarker"`
	MaxUploads         int              `xml:"MaxUploads"`
	IsTruncated        bool             `xml:"IsTruncated"`
	Uploads            []uploadListItem `xml:"Upload"`
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
	XMLName             xml.Name       `xml:"ListVersionsResult"`
	Xmlns               string         `xml:"xmlns,attr"`
	Name                string         `xml:"Name"`
	Prefix              string         `xml:"Prefix"`
	KeyMarker           string         `xml:"KeyMarker"`
	VersionIdMarker     string         `xml:"VersionIdMarker,omitempty"`
	NextKeyMarker       string         `xml:"NextKeyMarker,omitempty"`
	NextVersionIdMarker string         `xml:"NextVersionIdMarker,omitempty"`
	MaxKeys             int            `xml:"MaxKeys"`
	IsTruncated         bool           `xml:"IsTruncated"`
	Versions            []versionEntry `xml:"Version"`
	DeleteMarkers       []versionEntry `xml:"DeleteMarker,omitempty"`
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
			allUsersRead, allUsersWrite = allUsersPermission(g.Permission, allUsersRead, allUsersWrite)
		case authUsersURI:
			authRead = authRead || authUserPermission(g.Permission)
		}
	}
	return cannedFromFlags(allUsersRead, allUsersWrite, authRead)
}

func allUsersPermission(perm string, curRead, curWrite bool) (bool, bool) {
	switch perm {
	case "READ":
		return true, curWrite
	case "WRITE":
		return curRead, true
	case "FULL_CONTROL":
		return true, true
	}
	return curRead, curWrite
}

func authUserPermission(perm string) bool {
	return perm == "READ" || perm == "FULL_CONTROL"
}

func cannedFromFlags(allUsersRead, allUsersWrite, authRead bool) string {
	if allUsersRead && allUsersWrite {
		return "public-read-write"
	}
	if allUsersRead {
		return "public-read"
	}
	if authRead {
		return "authenticated-read"
	}
	return "private"
}

// locationConstraint is the body of GET /{bucket}?location. aero-vault is
// single-region; an empty constraint is what S3 clients read as us-east-1.
type locationConstraint struct {
	XMLName  xml.Name `xml:"LocationConstraint"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:",chardata"`
}

// --- Bucket CORS ---

type corsRule struct {
	XMLName        xml.Name `xml:"CORSRule"`
	AllowedOrigins []string `xml:"AllowedOrigins"`
	AllowedMethods []string `xml:"AllowedMethods"`
	AllowedHeaders []string `xml:"AllowedHeaders"`
	ExposeHeaders  []string `xml:"ExposeHeaders"`
	MaxAgeSeconds  int      `xml:"MaxAgeSeconds"`
}

type corsConfiguration struct {
	XMLName xml.Name   `xml:"CORSConfiguration"`
	Xmlns   string     `xml:"xmlns,attr,omitempty"`
	Rules   []corsRule `xml:"CORSRule"`
}

type corsInput struct {
	XMLName xml.Name   `xml:"CORSConfiguration"`
	Rules   []corsRule `xml:"CORSRule"`
}

// Server access logging types.
type bucketLoggingStatus struct {
	XMLName xml.Name       `xml:"BucketLoggingStatus"`
	Logging loggingEnabled `xml:"LoggingEnabled"`
}

type loggingEnabled struct {
	XMLName      xml.Name `xml:"LoggingEnabled"`
	TargetBucket string   `xml:"TargetBucket"`
	TargetPrefix string   `xml:"TargetPrefix"`
	TargetGrants []grant  `xml:"TargetGrants>Grant,omitempty"`
}

type grant struct {
	XMLName xml.Name `xml:"Grant"`
	Grantee struct {
		XMLName xml.Name `xml:",attr"`
		Type    string   `xml:"Type,attr"`
		ID      string   `xml:"Owner>ID,omitempty"`
	} `xml:"Grantee"`
}

// Notification XML types.
type notificationConfiguration struct {
	XMLName       xml.Name       `xml:"NotificationConfiguration"`
	Xmlns         string         `xml:"xmlns,attr,omitempty"`
	TopicConfigs  []topicConfig  `xml:"TopicConfiguration,omitempty"`
	QueueConfigs  []queueConfig  `xml:"QueueConfiguration,omitempty"`
	LambdaConfigs []lambdaConfig `xml:"LambdaFunctionConfiguration,omitempty"`
}

type topicConfig struct {
	ID       string   `xml:"Id,omitempty"`
	Events   []string `xml:"Event"`
	TopicARN string   `xml:"Topic"`
	Filter   *filter  `xml:"Filter,omitempty"`
}

type queueConfig struct {
	ID       string   `xml:"Id,omitempty"`
	Events   []string `xml:"Event"`
	QueueARN string   `xml:"Queue"`
	Filter   *filter  `xml:"Filter,omitempty"`
}

type lambdaConfig struct {
	ID        string   `xml:"Id,omitempty"`
	Events    []string `xml:"Event"`
	LambdaARN string   `xml:"LambdaFunctionArn"`
	Filter    *filter  `xml:"Filter,omitempty"`
}

type filter struct {
	S3Key filterRule `xml:"S3Key"`
}

type filterRule struct {
	Name  string    `xml:"Name"`
	Value filterVal `xml:"Value"`
}

type filterVal struct {
	Value string `xml:",chardata"`
}
