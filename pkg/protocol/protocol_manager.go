package protocol

import (
	"fmt"
	"sync"

	"github.com/iotaledger/hive.go/events"
	"github.com/iotaledger/hive.go/serializer/v2"
	"github.com/iotaledger/hornet/pkg/model/milestone"
	"github.com/iotaledger/hornet/pkg/model/storage"
	iotago "github.com/iotaledger/iota.go/v3"
)

func protoParasMsOptCaller(handler interface{}, params ...interface{}) {
	handler.(func(protoParas *iotago.ProtocolParamsMilestoneOpt))(params[0].(*iotago.ProtocolParamsMilestoneOpt))
}

// Events are events happening around the Manager.
type Events struct {
	// Emits protocol parameters for the unsupported milestone one milestone before.
	NextMilestoneUnsupported *events.Event
	// Emits critical errors.
	CriticalErrors *events.Event
}

// NewManager creates a new Manager.
func NewManager(storage *storage.Storage, ledgerIndex milestone.Index) (*Manager, error) {
	manager := &Manager{
		Events: &Events{
			NextMilestoneUnsupported: events.NewEvent(protoParasMsOptCaller),
			CriticalErrors:           events.NewEvent(events.ErrorCaller),
		},
		storage: storage,
		current: nil,
		pending: nil,
	}

	if err := manager.init(ledgerIndex); err != nil {
		return nil, err
	}

	return manager, nil
}

// Manager handles the knowledge about current, pending and supported protocol versions and parameters.
type Manager struct {
	// Events holds the events happening within the Manager.
	Events      *Events
	storage     *storage.Storage
	currentLock sync.RWMutex
	current     *iotago.ProtocolParameters
	pendingLock sync.RWMutex
	pending     []*iotago.ProtocolParamsMilestoneOpt
}

// init initialises the Manager by loading the last stored parameters and pending parameters.
func (m *Manager) init(ledgerIndex milestone.Index) error {
	m.currentLock.Lock()
	defer m.currentLock.Unlock()

	currentProtoParas, err := m.storage.ProtocolParameters(ledgerIndex)
	if err != nil {
		return err
	}

	protoParas := &iotago.ProtocolParameters{}
	if _, err := protoParas.Deserialize(currentProtoParas.Params, serializer.DeSeriModeNoValidation, nil); err != nil {
		return fmt.Errorf("failed to deserialize protocol parameters: %w", err)
	}

	m.current = protoParas
	m.loadPending(ledgerIndex)

	return nil
}

// loadPending initializes the pending protocol parameter changes from database.
func (m *Manager) loadPending(ledgerIndex milestone.Index) {
	m.pendingLock.Lock()
	defer m.pendingLock.Unlock()

	m.storage.ForEachProtocolParameters(func(protoParsMsOpt *iotago.ProtocolParamsMilestoneOpt) bool {
		if milestone.Index(protoParsMsOpt.TargetMilestoneIndex) > ledgerIndex {
			m.pending = append(m.pending, protoParsMsOpt)
		}
		return true
	})
}

func (m *Manager) readProtocolParasFromMilestone(index milestone.Index) *iotago.ProtocolParamsMilestoneOpt {
	cachedMs := m.storage.CachedMilestoneByIndexOrNil(index)
	if cachedMs == nil {
		return nil
	}
	defer cachedMs.Release(true)
	return cachedMs.Milestone().Milestone().Opts.MustSet().ProtocolParams()
}

// SupportedVersions returns a slice of supported protocol versions.
func (m *Manager) SupportedVersions() Versions {
	return SupportedVersions
}

// Current returns the current protocol parameters under which the node is operating.
func (m *Manager) Current() *iotago.ProtocolParameters {
	m.currentLock.RLock()
	defer m.currentLock.RUnlock()
	return m.current
}

// Pending returns the currently pending protocol changes.
func (m *Manager) Pending() []*iotago.ProtocolParamsMilestoneOpt {
	m.pendingLock.RLock()
	defer m.pendingLock.RUnlock()
	cpy := make([]*iotago.ProtocolParamsMilestoneOpt, len(m.pending))
	for i, ele := range m.pending {
		cpy[i] = ele.Clone().(*iotago.ProtocolParamsMilestoneOpt)
	}
	return cpy
}

// NextPendingSupported tells whether the next pending protocol parameters changes are supported.
func (m *Manager) NextPendingSupported() bool {
	m.pendingLock.RLock()
	defer m.pendingLock.RUnlock()
	if len(m.pending) == 0 {
		return true
	}
	return m.SupportedVersions().Supports(m.pending[0].ProtocolVersion)
}

// HandleConfirmedMilestone examines the newly confirmed milestone for protocol parameter changes.
func (m *Manager) HandleConfirmedMilestone(cachedMilestone *storage.CachedMilestone) {
	defer cachedMilestone.Release(true) // milestone -1
	ms := cachedMilestone.Milestone()

	if msProtoParas := ms.Milestone().Opts.MustSet().ProtocolParams(); msProtoParas != nil {
		m.pendingLock.Lock()
		m.pending = append(m.pending, msProtoParas)
		m.pendingLock.Unlock()

		if err := m.storage.StoreProtocolParameters(msProtoParas); err != nil {
			m.Events.CriticalErrors.Trigger(fmt.Errorf("unable to persist new protocol parameters: %w", err))
			return
		}
	}

	if !m.currentShouldChange(ms) {
		return
	}

	if err := m.updateCurrent(); err != nil {
		m.Events.CriticalErrors.Trigger(err)
		return
	}
}

// checks whether the current protocol parameters need to be updated.
func (m *Manager) currentShouldChange(milestone *storage.Milestone) bool {
	m.pendingLock.RLock()
	defer m.pendingLock.RUnlock()
	if len(m.pending) == 0 {
		return false
	}

	next := m.pending[0]

	switch {
	case next.TargetMilestoneIndex == milestone.Milestone().Index+1:
		if !m.SupportedVersions().Supports(next.ProtocolVersion) {
			m.Events.NextMilestoneUnsupported.Trigger(next)
		}
		return false
	case next.TargetMilestoneIndex > milestone.Milestone().Index:
		return false
	default:
		return true
	}
}

func (m *Manager) updateCurrent() error {
	m.currentLock.Lock()
	defer m.currentLock.Unlock()
	m.pendingLock.Lock()
	defer m.pendingLock.Unlock()

	nextMsProtoParamOpt := m.pending[0]
	nextParams := nextMsProtoParamOpt.Params

	// TODO: needs to be adapted for when protocol parameters struct changes
	nextProtoParams := &iotago.ProtocolParameters{}
	if _, err := nextProtoParams.Deserialize(nextParams, serializer.DeSeriModePerformValidation, nil); err != nil {
		return fmt.Errorf("unable to deserialize new protocol parameters: %w", err)
	}

	m.current = nextProtoParams
	m.pending = m.pending[1:]

	return nil
}
