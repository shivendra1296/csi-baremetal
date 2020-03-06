package base

import (
	"context"
	api "eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/generated/v1"
	accrd "eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/v1/availablecapacitycrd"
	"eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/v1/drivecrd"
	vcrd "eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/api/v1/volumecrd"
	"fmt"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

const (
	testNs             = "default"
	testID             = "someID"
	testNode1Name      = "node1"
	testDriveLocation1 = "drive"
)

var (
	testCtx    = context.Background()
	testUUID   = uuid.New().String()
	testVolume = vcrd.Volume{
		TypeMeta:   k8smetav1.TypeMeta{Kind: "Volume", APIVersion: "volume.dell.com/v1"},
		ObjectMeta: k8smetav1.ObjectMeta{Name: testID, Namespace: testNs},
		Spec: api.Volume{
			Id:       testID,
			Owner:    "pod",
			Size:     1000,
			Type:     "Type",
			Location: "location",
		},
	}

	testAC = accrd.AvailableCapacity{
		TypeMeta:   k8smetav1.TypeMeta{Kind: "AvailableCapacity", APIVersion: "availablecapacity.dell.com/v1"},
		ObjectMeta: k8smetav1.ObjectMeta{Name: testID, Namespace: testNs},
		Spec: api.AvailableCapacity{
			Size:     1024 * 1024,
			Type:     api.StorageClass_HDD,
			Location: testDriveLocation1,
			NodeId:   testNode1Name},
	}

	testDrive = drivecrd.Drive{
		TypeMeta:   k8smetav1.TypeMeta{Kind: "Drive", APIVersion: "drive.dell.com/v1"},
		ObjectMeta: k8smetav1.ObjectMeta{Name: testID, Namespace: testNs},
		Spec: api.Drive{
			UUID:         testUUID,
			VID:          "testVID",
			PID:          "testPID",
			SerialNumber: "testSN",
			Health:       0,
			Type:         0,
			Size:         1024 * 1024,
			Status:       0,
		},
	}
)

func TestKubernetesClient(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes client testing suite")
}

var _ = Describe("Working with CRD", func() {
	var k8sclient *KubeClient
	var err error
	BeforeEach(func() {
		k8sclient, err = GetFakeKubeClient(testNs)
		if err != nil {
			panic(err)
		}
	})
	AfterEach(func() {
		removeAllCrds(k8sclient)
	})

	Context("Create and read CRDs (volume, AC and drive)", func() {
		It("Should create and read Volume CR", func() {
			err := k8sclient.CreateCR(testCtx, &testVolume, testID)
			Expect(err).To(BeNil())
			rVolume := &vcrd.Volume{}
			err = k8sclient.ReadCR(testCtx, testID, rVolume)
			Expect(err).To(BeNil())
			Expect(rVolume.ObjectMeta.Name).To(Equal(testID))
		})

		It("Should create and read Available Capacity CR", func() {
			err := k8sclient.CreateCR(testCtx, &testAC, testID)
			Expect(err).To(BeNil())
			rAC := &accrd.AvailableCapacity{}
			err = k8sclient.ReadCR(testCtx, testID, rAC)
			Expect(err).To(BeNil())
			Expect(rAC.ObjectMeta.Name).To(Equal(testID))
		})

		It("Should create and read drive CR", func() {
			err := k8sclient.CreateCR(testCtx, &testDrive, testID)
			Expect(err).To(BeNil())
			rdrive := &drivecrd.Drive{}
			err = k8sclient.ReadCR(testCtx, testID, rdrive)
			Expect(err).To(BeNil())
			Expect(rdrive.ObjectMeta.Name).To(Equal(testID))
		})

		It("Should read volumes CR List", func() {
			err := k8sclient.CreateCR(context.Background(), &testVolume, testID)
			Expect(err).To(BeNil())

			vList := &vcrd.VolumeList{}
			err = k8sclient.ReadList(context.Background(), vList)
			Expect(err).To(BeNil())
			Expect(len(vList.Items)).To(Equal(1))
			Expect(vList.Items[0].Namespace).To(Equal(testNs))
		})

		It("Should read drive CR List", func() {
			err := k8sclient.CreateCR(testCtx, &testDrive, testID)
			Expect(err).To(BeNil())

			dList := &drivecrd.DriveList{}
			err = k8sclient.ReadList(context.Background(), dList)
			Expect(err).To(BeNil())
			Expect(len(dList.Items)).To(Equal(1))
			Expect(dList.Items[0].Namespace).To(Equal(testNs))
		})

		It("Try to read CRD that doesn't exist", func() {
			name := "notexistingcrd"
			ac := accrd.AvailableCapacity{}
			err := k8sclient.ReadCR(testCtx, name, &ac)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("\"%s\" not found", name)))
		})

	})

	Context("Update CR instance", func() {
		It("Should Available Capacity update successfully", func() {
			err := k8sclient.CreateCR(testCtx, &testAC, testID)
			Expect(err).To(BeNil())

			newSize := int64(1024 * 105)
			testAC.Spec.Size = newSize

			err = k8sclient.UpdateCR(testCtx, &testAC)
			Expect(err).To(BeNil())
			Expect(testAC.Spec.Size).To(Equal(newSize))

			acCopy := testAC.DeepCopy()
			err = k8sclient.Update(testCtx, &testAC)
			Expect(err).To(BeNil())
			Expect(&testAC).To(Equal(acCopy))
		})

		It("Should Drive update successfully", func() {
			err := k8sclient.CreateCR(testCtx, &testDrive, testUUID)
			Expect(err).To(BeNil())

			testDrive.Spec.Health = api.Health_BAD

			err = k8sclient.UpdateCR(testCtx, &testDrive)
			Expect(err).To(BeNil())
			Expect(testDrive.Spec.Health).To(Equal(api.Health_BAD))

			acCopy := testDrive.DeepCopy()
			err = k8sclient.Update(testCtx, &testDrive)
			Expect(err).To(BeNil())
			Expect(&testDrive).To(Equal(acCopy))
		})

		It("Update should fail", func() {

		})
	})
	Context("Delete CR", func() {
		It("AC should be deleted", func() {
			err := k8sclient.CreateCR(testCtx, &testAC, testUUID)
			Expect(err).To(BeNil())
			acList := accrd.AvailableCapacityList{}

			err = k8sclient.ReadList(testCtx, &acList)
			Expect(err).To(BeNil())
			Expect(len(acList.Items)).To(Equal(1))

			err = k8sclient.DeleteCR(testCtx, &testAC)
			Expect(err).To(BeNil())

			err = k8sclient.ReadList(testCtx, &acList)
			Expect(err).To(BeNil())
			Expect(len(acList.Items)).To(Equal(0))
		})
		It("Drive should be deleted", func() {
			err := k8sclient.CreateCR(testCtx, &testDrive, testUUID)
			Expect(err).To(BeNil())
			driveList := drivecrd.DriveList{}

			err = k8sclient.ReadList(testCtx, &driveList)
			Expect(err).To(BeNil())
			Expect(len(driveList.Items)).To(Equal(1))

			err = k8sclient.DeleteCR(testCtx, &testDrive)
			Expect(err).To(BeNil())

			err = k8sclient.ReadList(testCtx, &driveList)
			Expect(err).To(BeNil())
			Expect(len(driveList.Items)).To(Equal(0))
		})

	})
})

// remove all crds (volume and ac)
func removeAllCrds(s *KubeClient) {
	var (
		vList  = &vcrd.VolumeList{}
		acList = &accrd.AvailableCapacityList{}
		err    error
	)

	if err = s.ReadList(testCtx, vList); err != nil {
		Fail(fmt.Sprintf("unable to read volume crds list: %v", err))
	}

	if err = s.ReadList(testCtx, acList); err != nil {
		Fail(fmt.Sprintf("unable to read available capacity crds list: %v", err))
	}

	// remove all volume crds
	for _, obj := range vList.Items {
		if err = s.Delete(testCtx, &obj); err != nil {
			Fail(fmt.Sprintf("unable to delete volume crd: %v", err))
		}
	}

	// remove all ac crds
	for _, obj := range acList.Items {
		if err = s.Delete(testCtx, &obj); err != nil {
			Fail(fmt.Sprintf("unable to delete ac crd: %v", err))
		}
	}
}
