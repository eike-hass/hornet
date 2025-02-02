package inx

import (
	"context"

	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iotaledger/hive.go/contextutils"
	"github.com/iotaledger/hive.go/events"
	"github.com/iotaledger/hive.go/serializer/v2"
	"github.com/iotaledger/hive.go/workerpool"
	"github.com/iotaledger/hornet/v2/pkg/common"
	"github.com/iotaledger/hornet/v2/pkg/model/storage"
	"github.com/iotaledger/hornet/v2/pkg/tangle"
	"github.com/iotaledger/hornet/v2/pkg/tipselect"
	inx "github.com/iotaledger/inx/go"
	iotago "github.com/iotaledger/iota.go/v3"
)

func INXNewBlockMetadata(blockID iotago.BlockID, metadata *storage.BlockMetadata, tip ...*tipselect.Tip) (*inx.BlockMetadata, error) {
	m := &inx.BlockMetadata{
		BlockId: inx.NewBlockId(blockID),
		Parents: inx.NewBlockIds(metadata.Parents()),
		Solid:   metadata.IsSolid(),
	}

	referenced, msIndex, wfIndex := metadata.ReferencedWithIndexAndWhiteFlagIndex()
	if referenced {
		m.ReferencedByMilestoneIndex = msIndex
		m.WhiteFlagIndex = wfIndex
		inclusionState := inx.BlockMetadata_NO_TRANSACTION
		conflict := metadata.Conflict()
		if conflict != storage.ConflictNone {
			inclusionState = inx.BlockMetadata_CONFLICTING
			m.ConflictReason = inx.BlockMetadata_ConflictReason(conflict)
		} else if metadata.IsIncludedTxInLedger() {
			inclusionState = inx.BlockMetadata_INCLUDED
		}
		m.LedgerInclusionState = inclusionState

		if metadata.IsMilestone() {
			cachedBlock := deps.Storage.CachedBlockOrNil(blockID)
			if cachedBlock == nil {
				return nil, status.Errorf(codes.NotFound, "block not found: %s", blockID.ToHex())
			}
			defer cachedBlock.Release(true)

			milestone := cachedBlock.Block().Milestone()
			if milestone == nil {
				return nil, status.Errorf(codes.NotFound, "milestone for block not found: %s", blockID.ToHex())
			}
			m.MilestoneIndex = milestone.Index
		}

		return m, nil
	}

	if metadata.IsSolid() {

		if len(tip) > 0 {
			switch tip[0].Score {
			case tipselect.ScoreLazy:
				// promote is false
				m.ShouldReattach = true
			case tipselect.ScoreSemiLazy:
				m.ShouldPromote = true
				// reattach is false
			case tipselect.ScoreNonLazy:
				// promote is false
				// reattach is false
			}
			return m, nil
		}

		// determine info about the quality of the tip if not referenced
		cmi := deps.SyncManager.ConfirmedMilestoneIndex()

		tipScore, err := deps.TipScoreCalculator.TipScore(Plugin.Daemon().ContextStopped(), blockID, cmi)
		if err != nil {
			if errors.Is(err, common.ErrOperationAborted) {
				return nil, status.Errorf(codes.Unavailable, err.Error())
			}
			return nil, status.Errorf(codes.Internal, err.Error())
		}

		switch tipScore {
		case tangle.TipScoreNotFound:
			return nil, status.Errorf(codes.Internal, "tip score could not be calculated")
		case tangle.TipScoreOCRIThresholdReached, tangle.TipScoreYCRIThresholdReached:
			m.ShouldPromote = true
			// reattach is false
		case tangle.TipScoreBelowMaxDepth:
			// promote is false
			m.ShouldReattach = true
		case tangle.TipScoreHealthy:
			// promote is false
			// reattach is false
		}
	}

	return m, nil
}

func (s *INXServer) ReadBlock(_ context.Context, blockID *inx.BlockId) (*inx.RawBlock, error) {
	blkId := blockID.Unwrap()
	cachedBlock := deps.Storage.CachedBlockOrNil(blkId) // block +1
	if cachedBlock == nil {
		return nil, status.Errorf(codes.NotFound, "block %s not found", blkId.ToHex())
	}
	defer cachedBlock.Release(true) // block -1
	return inx.WrapBlock(cachedBlock.Block().Block())
}

func (s *INXServer) ReadBlockMetadata(_ context.Context, blockID *inx.BlockId) (*inx.BlockMetadata, error) {
	blkId := blockID.Unwrap()
	cachedBlockMeta := deps.Storage.CachedBlockMetadataOrNil(blkId) // meta +1
	if cachedBlockMeta == nil {
		isSolidEntryPoint, err := deps.Storage.SolidEntryPointsContain(blkId)
		if err == nil && isSolidEntryPoint {
			return &inx.BlockMetadata{
				BlockId: blockID,
				Solid:   true,
			}, nil
		}
		return nil, status.Errorf(codes.NotFound, "block metadata %s not found", blkId.ToHex())
	}
	defer cachedBlockMeta.Release(true) // meta -1
	return INXNewBlockMetadata(cachedBlockMeta.Metadata().BlockID(), cachedBlockMeta.Metadata())
}

func (s *INXServer) ListenToBlocks(_ *inx.NoParams, srv inx.INX_ListenToBlocksServer) error {
	ctx, cancel := context.WithCancel(context.Background())
	wp := workerpool.New(func(task workerpool.Task) {
		cachedBlock := task.Param(0).(*storage.CachedBlock)
		defer cachedBlock.Release(true) // block -1

		payload := inx.NewBlockWithBytes(cachedBlock.Block().BlockID(), cachedBlock.Block().Data())
		if err := srv.Send(payload); err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
		}
		task.Return(nil)
	}, workerpool.WorkerCount(workerCount), workerpool.QueueSize(workerQueueSize), workerpool.FlushTasksAtShutdown(true))
	closure := events.NewClosure(func(cachedBlock *storage.CachedBlock, latestMilestoneIndex iotago.MilestoneIndex, confirmedMilestoneIndex iotago.MilestoneIndex) {
		wp.Submit(cachedBlock)
	})
	wp.Start()
	deps.Tangle.Events.ReceivedNewBlock.Attach(closure)
	<-ctx.Done()
	deps.Tangle.Events.ReceivedNewBlock.Detach(closure)
	wp.Stop()
	return ctx.Err()
}

func (s *INXServer) ListenToSolidBlocks(_ *inx.NoParams, srv inx.INX_ListenToSolidBlocksServer) error {
	ctx, cancel := context.WithCancel(context.Background())
	wp := workerpool.New(func(task workerpool.Task) {
		blockMeta := task.Param(0).(*storage.CachedMetadata)
		defer blockMeta.Release(true) // meta -1

		payload, err := INXNewBlockMetadata(blockMeta.Metadata().BlockID(), blockMeta.Metadata())
		if err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
			return
		}
		if err := srv.Send(payload); err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
		}
		task.Return(nil)
	}, workerpool.WorkerCount(workerCount), workerpool.QueueSize(workerQueueSize), workerpool.FlushTasksAtShutdown(true))
	closure := events.NewClosure(func(blockMeta *storage.CachedMetadata) {
		wp.Submit(blockMeta)
	})
	wp.Start()
	deps.Tangle.Events.BlockSolid.Attach(closure)
	<-ctx.Done()
	deps.Tangle.Events.BlockSolid.Detach(closure)
	wp.Stop()
	return ctx.Err()
}

func (s *INXServer) ListenToReferencedBlocks(_ *inx.NoParams, srv inx.INX_ListenToReferencedBlocksServer) error {
	ctx, cancel := context.WithCancel(context.Background())
	wp := workerpool.New(func(task workerpool.Task) {
		blockMeta := task.Param(0).(*storage.CachedMetadata)
		defer blockMeta.Release(true) // meta -1

		payload, err := INXNewBlockMetadata(blockMeta.Metadata().BlockID(), blockMeta.Metadata())
		if err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
			return
		}
		if err := srv.Send(payload); err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
		}
		task.Return(nil)
	}, workerpool.WorkerCount(workerCount), workerpool.QueueSize(workerQueueSize), workerpool.FlushTasksAtShutdown(true))
	closure := events.NewClosure(func(blockMeta *storage.CachedMetadata, index iotago.MilestoneIndex, confTime uint32) {
		wp.Submit(blockMeta)
	})
	wp.Start()
	deps.Tangle.Events.BlockReferenced.Attach(closure)
	<-ctx.Done()
	deps.Tangle.Events.BlockReferenced.Detach(closure)
	wp.Stop()
	return ctx.Err()
}

func (s *INXServer) ListenToTipScoreUpdates(_ *inx.NoParams, srv inx.INX_ListenToTipScoreUpdatesServer) error {
	ctx, cancel := context.WithCancel(context.Background())
	wp := workerpool.New(func(task workerpool.Task) {
		tip := task.Param(0).(*tipselect.Tip)

		blockMeta := deps.Storage.CachedBlockMetadataOrNil(tip.BlockID)
		if blockMeta == nil {
			return
		}
		defer blockMeta.Release(true) // meta -1

		payload, err := INXNewBlockMetadata(blockMeta.Metadata().BlockID(), blockMeta.Metadata(), tip)
		if err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
			return
		}
		if err := srv.Send(payload); err != nil {
			Plugin.LogInfof("Send error: %v", err)
			cancel()
		}
		task.Return(nil)
	}, workerpool.WorkerCount(workerCount), workerpool.QueueSize(workerQueueSize), workerpool.FlushTasksAtShutdown(true))

	closure := events.NewClosure(func(tip *tipselect.Tip) { wp.Submit(tip) })
	wp.Start()
	deps.TipSelector.Events.TipAdded.Attach(closure)
	deps.TipSelector.Events.TipRemoved.Attach(closure)
	<-ctx.Done()
	deps.TipSelector.Events.TipAdded.Detach(closure)
	deps.TipSelector.Events.TipRemoved.Detach(closure)
	wp.Stop()
	return ctx.Err()
}

func (s *INXServer) SubmitBlock(context context.Context, rawBlock *inx.RawBlock) (*inx.BlockId, error) {
	block, err := rawBlock.UnwrapBlock(serializer.DeSeriModeNoValidation, nil)
	if err != nil {
		return nil, err
	}

	mergedCtx, mergedCtxCancel := contextutils.MergeContexts(context, Plugin.Daemon().ContextStopped())
	defer mergedCtxCancel()

	blockID, err := attacher.AttachBlock(mergedCtx, block)
	if err != nil {
		return nil, err
	}
	return inx.NewBlockId(blockID), nil
}
