package server

import (
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEarlyQuit = "EarlyQuit"
	StorageKeyEarlyQuit        = "statistics"
	MinEarlyQuitPenaltyLevel   = -1
	MaxEarlyQuitPenaltyLevel   = 3
)

type EarlyQuitConfig struct {
	sync.Mutex
	EarlyQuitPenaltyLevel int32     `json:"early_quit_penalty_level"`
	LastEarlyQuitTime     time.Time `json:"last_early_quit_time"`
	version               string
}

func NewEarlyQuitConfig() *EarlyQuitConfig {
	return &EarlyQuitConfig{}
}

func (s *EarlyQuitConfig) StorageMeta() StorableMetadata {
	s.Lock()
	defer s.Unlock()
	return StorableMetadata{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s *EarlyQuitConfig) SetStorageMeta(meta StorableMetadata) {
	s.Lock()
	defer s.Unlock()
	s.version = meta.Version
}

func (s *EarlyQuitConfig) GetStorageVersion() string {
	s.Lock()
	defer s.Unlock()
	return s.version
}

func (s *EarlyQuitConfig) IncrementEarlyQuit() {
	s.Lock()
	defer s.Unlock()
	s.LastEarlyQuitTime = time.Now().UTC()
	s.EarlyQuitPenaltyLevel++
	if s.EarlyQuitPenaltyLevel > MaxEarlyQuitPenaltyLevel {
		s.EarlyQuitPenaltyLevel = MaxEarlyQuitPenaltyLevel // Max penalty level
	}
}

func (s *EarlyQuitConfig) IncrementCompletedMatches() {
	s.Lock()
	defer s.Unlock()
	s.LastEarlyQuitTime = time.Time{}
	s.EarlyQuitPenaltyLevel--
	if s.EarlyQuitPenaltyLevel < MinEarlyQuitPenaltyLevel {
		s.EarlyQuitPenaltyLevel = MinEarlyQuitPenaltyLevel // Reset penalty level
	}
}

func (s *EarlyQuitConfig) GetPenaltyLevel() int {
	s.Lock()
	defer s.Unlock()
	return int(s.EarlyQuitPenaltyLevel)
}
