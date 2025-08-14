package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StorableAdapter provides an adapter pattern for making any object storable
type StorableAdapter struct {
	data            interface{}
	collection      string
	key             string
	permissionRead  int
	permissionWrite int
	version         string
	indexes         []StorableIndexMeta
	versionSetter   func(userID, version string)
}

// StorableIndexMeta represents an index configuration for storage
type StorableIndexMeta struct {
	Name           string
	Collection     string
	Key            string
	Fields         []string
	SortableFields []string
	MaxEntries     int
	IndexOnly      bool
}

// StorableMeta contains metadata for storage operations
type StorableMeta struct {
	Collection      string
	Key             string
	PermissionRead  int
	PermissionWrite int
	Version         string
}

func (s StorableMeta) String() string {
	return fmt.Sprintf("%s:%s", s.Collection, s.Key)
}

// NewStorableAdapter creates a new StorableAdapter for the given data
func NewStorableAdapter(data interface{}, collection, key string) *StorableAdapter {
	return &StorableAdapter{
		data:            data,
		collection:      collection,
		key:             key,
		permissionRead:  0,  // Default read permission
		permissionWrite: 0,  // Default write permission
		version:         "*", // Default version for new objects
	}
}

// WithPermissions sets the read and write permissions
func (s *StorableAdapter) WithPermissions(read, write int) *StorableAdapter {
	s.permissionRead = read
	s.permissionWrite = write
	return s
}

// WithVersion sets the version for optimistic concurrency control
func (s *StorableAdapter) WithVersion(version string) *StorableAdapter {
	s.version = version
	return s
}

// WithIndexes sets the storage indexes
func (s *StorableAdapter) WithIndexes(indexes []StorableIndexMeta) *StorableAdapter {
	s.indexes = indexes
	return s
}

// WithVersionSetter sets a function to be called when the version is updated
func (s *StorableAdapter) WithVersionSetter(setter func(userID, version string)) *StorableAdapter {
	s.versionSetter = setter
	return s
}

// Data returns the underlying data object
func (s *StorableAdapter) Data() interface{} {
	return s.data
}

// Meta returns the storage metadata
func (s *StorableAdapter) Meta() StorableMeta {
	return StorableMeta{
		Collection:      s.collection,
		Key:             s.key,
		PermissionRead:  s.permissionRead,
		PermissionWrite: s.permissionWrite,
		Version:         s.version,
	}
}

// Indexes returns the storage indexes if any
func (s *StorableAdapter) Indexes() []StorableIndexMeta {
	return s.indexes
}

// HasIndexes returns true if this adapter has storage indexes
func (s *StorableAdapter) HasIndexes() bool {
	return len(s.indexes) > 0
}

// IsVersioned returns true if this adapter supports versioning
func (s *StorableAdapter) IsVersioned() bool {
	return s.versionSetter != nil
}

// SetVersion updates the version and calls the version setter if available
func (s *StorableAdapter) SetVersion(userID, version string) {
	s.version = version
	if s.versionSetter != nil {
		s.versionSetter(userID, version)
	}
}

// StorableRead reads an object from storage using the adapter pattern
func StorableRead(ctx context.Context, nk runtime.NakamaModule, userID string, adapter *StorableAdapter, create bool) error {
	if adapter == nil || adapter.data == nil {
		return status.Errorf(codes.InvalidArgument, "adapter or data is nil")
	}

	// Check if the data is a pointer
	if dataValue := reflect.ValueOf(adapter.data); dataValue.Kind() != reflect.Ptr {
		return status.Errorf(codes.InvalidArgument, "data is not a pointer")
	}

	meta := adapter.Meta()

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: meta.Collection,
			Key:        meta.Key,
			UserID:     userID,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to read %s/%s: %v", userID, meta.String(), err)
	}

	if len(objs) != 0 {
		if err = json.Unmarshal([]byte(objs[0].GetValue()), adapter.data); err != nil {
			return status.Errorf(codes.Internal, "failed to unmarshal %s/%s: %v", userID, meta.String(), err)
		}

		// Update version if adapter supports versioning
		if adapter.IsVersioned() {
			adapter.SetVersion(userID, objs[0].GetVersion())
		}
	} else {
		if create {
			// Set version for new objects if adapter supports versioning
			if adapter.IsVersioned() {
				adapter.SetVersion(userID, "*")
			}

			if err = StorableWrite(ctx, nk, userID, adapter); err != nil {
				return status.Errorf(codes.Internal, "failed to create %s/%s: %v", userID, meta.String(), err)
			}
		} else {
			return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
		}
	}

	return nil
}

// StorableWrite writes an object to storage using the adapter pattern
func StorableWrite(ctx context.Context, nk runtime.NakamaModule, userID string, adapter *StorableAdapter) error {
	if adapter == nil || adapter.data == nil {
		return status.Errorf(codes.InvalidArgument, "adapter or data is nil")
	}

	meta := adapter.Meta()
	data, err := json.Marshal(adapter.data)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to marshal %s/%s: %s", userID, meta.String(), err.Error())
	}

	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      meta.Collection,
			Key:             meta.Key,
			UserID:          userID,
			Value:           string(data),
			Version:         meta.Version,
			PermissionRead:  meta.PermissionRead,
			PermissionWrite: meta.PermissionWrite,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to write %s/%s: %v", userID, meta.String(), err.Error())
	}

	// Update version if adapter supports versioning
	if adapter.IsVersioned() {
		adapter.SetVersion(userID, acks[0].GetVersion())
	}

	return nil
}