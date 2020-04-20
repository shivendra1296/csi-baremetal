package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	coreV1 "k8s.io/api/core/v1"
	k8sError "k8s.io/apimachinery/pkg/api/errors"
	k8s "sigs.k8s.io/controller-runtime/pkg/client"

	api "eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/generated/v1"
	apiV1 "eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/v1"
	"eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/v1/volumecrd"
	"eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/pkg/base"
	"eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/pkg/common"
)

type NodeID string

const (
	NodeSvcPodsMask   = "baremetal-csi-node"
	NodeIDTopologyKey = "baremetal-csi/nodeid"
)

// interface implementation for ControllerServer
type CSIControllerService struct {
	k8sclient *base.KubeClient

	//mutex for csi request
	reqMu sync.Mutex
	log   *logrus.Entry

	svc        common.VolumeOperations
	acProvider common.AvailableCapacityOperations
}

func NewControllerService(k8sClient *base.KubeClient, logger *logrus.Logger) *CSIControllerService {
	c := &CSIControllerService{
		k8sclient:  k8sClient,
		acProvider: common.NewACOperationsImpl(k8sClient, logger),
		svc:        common.NewVolumeOperationsImpl(k8sClient, logger),
	}
	c.log = logger.WithField("component", "CSIControllerService")
	return c
}

// WaitNodeServices waits for the first ready Node
// return true in case of ready node service and false instead
func (c *CSIControllerService) WaitNodeServices() bool {
	ll := c.log.WithField("method", "WaitNodeServices")

	timeout := 240 * time.Second
	ctx, cancelFn := context.WithTimeout(context.Background(), timeout)
	defer cancelFn()

	pods, err := c.getPods(ctx, NodeSvcPodsMask)

	if err != nil {
		ll.Infof("Unable to detect pods with node service: %v", err)
		return false
	}

	ll.Infof("Found %d pods with Node service", len(pods))

	for _, pod := range pods {
		// Consider the Node pod is ready if all of its containers are ready.
		// Not check Running state because Node can be in it even if all containers are not ready.
		containersReady := true
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if !containerStatus.Ready {
				containersReady = false
			}
		}
		if containersReady {
			return true
		}
	}

	return false
}

func (c *CSIControllerService) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	ll := c.log.WithFields(logrus.Fields{
		"method":   "CreateVolume",
		"volumeID": req.GetName(),
	})
	ll.Infof("Processing request: %v", req)

	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume name missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	preferredNode := ""
	if req.GetAccessibilityRequirements() != nil && len(req.GetAccessibilityRequirements().Preferred) > 0 {
		preferredNode = req.GetAccessibilityRequirements().Preferred[0].Segments[NodeIDTopologyKey]
		ll.Infof("Preferred node was provided: %s", preferredNode)
	}

	var (
		err error
		vol *api.Volume
	)

	c.reqMu.Lock()
	vol, err = c.svc.CreateVolume(ctx, api.Volume{
		Id:           req.Name,
		StorageClass: base.ConvertStorageClass(req.Parameters["storageType"]),
		NodeId:       preferredNode,
		Size:         req.GetCapacityRange().GetRequiredBytes(),
	})
	c.reqMu.Unlock()

	if err != nil {
		return nil, err
	}

	var newStatus = vol.CSIStatus
	if vol.CSIStatus == apiV1.Creating {
		ll.Info("Waiting until volume will reach Created or Failed status")
		reached, st := c.svc.WaitStatus(ctx, req.GetName(), apiV1.Created, apiV1.Failed)
		if !reached {
			return nil, status.Errorf(codes.Aborted, "CreateVolume is in progress")
		}
		newStatus = st
	}

	if err = c.acProvider.DeleteIfEmpty(ctx, vol.Location); err != nil {
		ll.Errorf("Unable to check AC size by location: %v", err)
	}

	if newStatus != apiV1.Created {
		ll.Errorf("Unable to create volume %v. Volume reached %s status", vol, newStatus)
		return nil, status.Error(codes.Internal, "Unable to create volume on local node.")
	}

	ll.Infof("Construct response based on volume: %v", vol)
	topologyList := []*csi.Topology{
		{Segments: map[string]string{NodeIDTopologyKey: vol.NodeId}},
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:           req.Name,
			CapacityBytes:      vol.Size,
			VolumeContext:      req.GetParameters(),
			AccessibleTopology: topologyList,
		},
	}, nil
}

func (c *CSIControllerService) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	ll := c.log.WithFields(logrus.Fields{
		"method":   "DeleteVolume",
		"volumeID": req.GetVolumeId(),
	})

	ll.Infof("Processing request: %v", req)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID must be provided")
	}
	ctx = context.WithValue(ctx, base.RequestUUID, req.VolumeId)

	c.reqMu.Lock()
	err := c.svc.DeleteVolume(ctx, req.GetVolumeId())
	c.reqMu.Unlock()

	if err != nil {
		if k8sError.IsNotFound(err) {
			ll.Infof("Volume doesn't exist")
			return &csi.DeleteVolumeResponse{}, nil
		}
		ll.Errorf("Unable to delete volume: %v", err)
		return nil, err
	}

	ll.Info("Waiting until volume will reach Removed status")
	reached, st := c.svc.WaitStatus(ctx, req.VolumeId, apiV1.Failed, apiV1.Removed)

	if !reached {
		return nil, fmt.Errorf("unable to delete volume %s, still in removing state", req.VolumeId)
	}

	if st == apiV1.Failed {
		return nil, status.Error(codes.Internal, "volume has reached FailToRemove status")
	}

	c.reqMu.Lock()
	c.svc.UpdateCRsAfterVolumeDeletion(ctx, req.VolumeId)
	c.reqMu.Unlock()

	return &csi.DeleteVolumeResponse{}, nil
}

func (c *CSIControllerService) ControllerPublishVolume(ctx context.Context,
	req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	ll := c.log.WithFields(logrus.Fields{
		"method":   "ControllerPublishVolume",
		"volumeID": req.GetVolumeId(),
	})

	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume: Node ID must be provided")
	}

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume: Volume ID must be provided")
	}

	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume: Volume capabilities"+
			" must be provided")
	}

	vol := &volumecrd.Volume{}
	if err := c.k8sclient.ReadCR(ctx, req.VolumeId, vol); err != nil {
		if k8sError.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "Volume is not found")
		}
		ll.Errorf("k8s client can't read volume CR: %v", err)
		return nil, status.Error(codes.Unavailable, "Something went wrong with k8s client")
	}

	ll.Info("Return empty response, ok.")

	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (c *CSIControllerService) ControllerUnpublishVolume(ctx context.Context,
	req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	ll := c.log.WithFields(logrus.Fields{
		"method":   "ControllerUnpublishVolume",
		"volumeID": req.GetVolumeId(),
	})

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume: Volume ID must be provided")
	}

	ll.Info("Return empty response, ok")

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (c *CSIControllerService) ValidateVolumeCapabilities(context.Context, *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) ListVolumes(context.Context, *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) GetCapacity(context.Context, *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	ll := c.log.WithFields(logrus.Fields{
		"method": "ControllerGetCapabilities",
	})

	newCap := func(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
		return &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: cap,
				},
			},
		}
	}

	caps := make([]*csi.ControllerServiceCapability, 0)
	for _, c := range []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	} {
		caps = append(caps, newCap(c))
	}

	resp := &csi.ControllerGetCapabilitiesResponse{
		Capabilities: caps,
	}

	ll.Infof("ControllerGetCapabilities returns response: %v", resp)

	return resp, nil
}

func (c *CSIControllerService) CreateSnapshot(context.Context, *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) DeleteSnapshot(context.Context, *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) ListSnapshots(context.Context, *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) ControllerExpandVolume(context.Context, *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented yet")
}

func (c *CSIControllerService) getPods(ctx context.Context, mask string) ([]*coreV1.Pod, error) {
	pods := coreV1.PodList{}

	if err := c.k8sclient.List(ctx, &pods, k8s.InNamespace(c.k8sclient.Namespace)); err != nil {
		return nil, err
	}
	p := make([]*coreV1.Pod, 0)
	for i := range pods.Items {
		podName := pods.Items[i].ObjectMeta.Name
		if strings.Contains(podName, mask) {
			p = append(p, &pods.Items[i])
		}
	}
	return p, nil
}
