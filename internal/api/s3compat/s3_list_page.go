package s3compat

import (
	"fmt"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

const objectListScanPageSize = 1000

type objectListFetcher func(marker string, limit int) (repository.ListPage, error)

type objectListPage struct {
	Objects        []repository.Object
	CommonPrefixes []commonPrefix
	NextMarker     string
	HasMore        bool
}

type delimiterListBuilder struct {
	page               objectListPage
	prefix             string
	delimiter          string
	marker             string
	activePrefix       string
	lastIncludedPrefix string
	maxKeys            int
}

func (p objectListPage) keyCount() int {
	return len(p.Objects) + len(p.CommonPrefixes)
}

func loadObjectListPage(
	prefix, delimiter, marker string,
	maxKeys int,
	fetch objectListFetcher,
) (objectListPage, error) {
	if maxKeys == 0 {
		return objectListPage{}, nil
	}
	if delimiter == "" {
		page, err := fetch(marker, maxKeys)
		return objectListPage{
			Objects: page.Objects, NextMarker: page.NextMarker, HasMore: page.HasMore,
		}, err
	}
	return loadDelimitedObjectListPage(prefix, delimiter, marker, maxKeys, fetch)
}

func loadDelimitedObjectListPage(
	prefix, delimiter, marker string,
	maxKeys int,
	fetch objectListFetcher,
) (objectListPage, error) {
	builder := delimiterListBuilder{
		prefix: prefix, delimiter: delimiter, marker: marker, maxKeys: maxKeys,
	}
	cursor := marker
	for {
		page, err := fetch(cursor, objectListScanPageSize)
		if err != nil {
			return objectListPage{}, err
		}
		for _, obj := range page.Objects {
			if builder.append(obj) {
				builder.page.HasMore = true
				return builder.page, nil
			}
		}
		if !page.HasMore {
			return builder.page, nil
		}
		if page.NextMarker == "" || page.NextMarker == cursor {
			return objectListPage{}, fmt.Errorf("list objects pagination did not advance")
		}
		cursor = page.NextMarker
	}
}

func (b *delimiterListBuilder) append(obj repository.Object) bool {
	value, grouped := collapseListKey(obj.Key, b.prefix, b.delimiter)
	if grouped && value == b.activePrefix {
		if value == b.lastIncludedPrefix {
			b.page.NextMarker = obj.Key
		}
		return false
	}
	b.activePrefix = ""
	if grouped {
		b.activePrefix = value
		if value <= b.marker {
			return false
		}
	}
	if b.page.keyCount() == b.maxKeys {
		return true
	}
	b.page.NextMarker = obj.Key
	b.lastIncludedPrefix = ""
	if grouped {
		b.page.CommonPrefixes = append(b.page.CommonPrefixes, commonPrefix{Prefix: value})
		b.lastIncludedPrefix = value
	} else {
		b.page.Objects = append(b.page.Objects, obj)
	}
	return false
}

func collapseListKey(key, prefix, delimiter string) (string, bool) {
	remainder := strings.TrimPrefix(key, prefix)
	if index := strings.Index(remainder, delimiter); index >= 0 {
		return prefix + remainder[:index+len(delimiter)], true
	}
	return key, false
}

func listContents(objects []repository.Object) []listContent {
	contents := make([]listContent, 0, len(objects))
	for _, object := range objects {
		contents = append(contents, listContent{
			Key:          object.Key,
			LastModified: object.UpdatedAt.UTC(),
			ETag:         `"` + object.ETag + `"`,
			Size:         object.Size,
			StorageClass: service.StorageClassOrDefault(object.StorageClass),
		})
	}
	return contents
}
