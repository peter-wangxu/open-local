/*
Copyright © 2021 Alibaba Group Holding Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"github.com/alibaba/open-local/pkg"
	"github.com/alibaba/open-local/pkg/utils"
	"github.com/stretchr/testify/assert"
	"reflect"
	"testing"
	"time"

	snapshotfake "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned/fake"
	snapshotinformers "github.com/kubernetes-csi/external-snapshotter/client/v4/informers/externalversions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"

	localv1alpha1 "github.com/alibaba/open-local/pkg/apis/storage/v1alpha1"
	localfake "github.com/alibaba/open-local/pkg/generated/clientset/versioned/fake"
	informers "github.com/alibaba/open-local/pkg/generated/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client     *localfake.Clientset
	kubeclient *k8sfake.Clientset
	snapclient *snapshotfake.Clientset
	// Objects to put in the store.
	nlsLister  []*localv1alpha1.NodeLocalStorage
	nlscLister []*localv1alpha1.NodeLocalStorageInitConfig
	nodeLister []*corev1.Node
	// Actions expected to happen on the client.
	kubeactions  []core.Action
	localactions []core.Action
	// Objects from here preloaded into NewSimpleFake.
	kubeobjects  []runtime.Object
	localobjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.localobjects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	return f
}

func newNLSForAllocate(name string) *localv1alpha1.NodeLocalStorage {
	nls := newNLS(name)
	allocateInfos := &pkg.NodeStorageAllocateInfo{
		PvcAllocates: map[string]pkg.NodeStoragePVCAllocateInfo{
			utils.GetPVCKey(utils.LocalNameSpace, utils.PVCWithVG): {
				PVCNameSpace: utils.LocalNameSpace,
				PVCName:      utils.PVCWithVG,
				PVAllocatedInfo: pkg.PVAllocatedInfo{
					VGName:     utils.VGSSD,
					VolumeType: string(pkg.VolumeTypeLVM),
				},
			},
			utils.GetPVCKey(utils.LocalNameSpace, utils.PVCWithDevice): {
				PVCNameSpace: utils.LocalNameSpace,
				PVCName:      utils.PVCWithDevice,
				PVAllocatedInfo: pkg.PVAllocatedInfo{
					DeviceName: "/dev/sdc",
					VolumeType: string(pkg.VolumeTypeDevice),
				},
			},
		},
	}
	allocateInfoJson, _ := json.Marshal(allocateInfos)
	nls.Annotations = map[string]string{
		pkg.AnnotationNodeStorageAllocatedInfoKey: string(allocateInfoJson),
	}
	return nls
}

func newNLS(name string) *localv1alpha1.NodeLocalStorage {

	return &localv1alpha1.NodeLocalStorage{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: localv1alpha1.NodeLocalStorageSpec{
			NodeName: name,
		},
	}
}

func newNLSC(name string) *localv1alpha1.NodeLocalStorageInitConfig {

	return &localv1alpha1.NodeLocalStorageInitConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: localv1alpha1.NodeLocalStorageInitConfigSpec{
			GlobalConfig: localv1alpha1.GlobalConfig{
				ListConfig: localv1alpha1.ListConfig{
					VGs: localv1alpha1.VGList{
						Include: []string{"yoda-pool"},
					},
				},
			},
			NodesConfig: []localv1alpha1.NodeConfig{
				{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"beta.kubernetes.io/os": "linux",
						},
					},
					ListConfig: localv1alpha1.ListConfig{
						VGs: localv1alpha1.VGList{
							Include: []string{"open-local-pool-0"},
						},
					},
				},
				{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"node-role.kubernetes.io/master": "",
						},
					},
					ListConfig: localv1alpha1.ListConfig{
						VGs: localv1alpha1.VGList{
							Include: []string{"open-local-pool-1"},
						},
					},
				},
			},
		},
	}

}

func newMasterNode(name string) *corev1.Node {
	labels := map[string]string{
		"node-role.kubernetes.io/master": "",
		"beta.kubernetes.io/os":          "linux",
		"kubernetes.io/hostname":         name,
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func (f *fixture) newController() (*Controller, informers.SharedInformerFactory, kubeinformers.SharedInformerFactory) {
	f.client = localfake.NewSimpleClientset(f.localobjects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)
	f.snapclient = snapshotfake.NewSimpleClientset()

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())
	snapI := snapshotinformers.NewSharedInformerFactory(f.snapclient, noResyncPeriodFunc())
	c := NewController(f.kubeclient, f.client, f.snapclient, k8sI, i, snapI, "open-local")

	c.nlsSynced = alwaysReady
	c.nlscSynced = alwaysReady
	c.nodeSynced = alwaysReady
	c.pvcSynced = alwaysReady
	c.pvSynced = alwaysReady
	c.snapshotSynced = alwaysReady
	c.snapshotContentSynced = alwaysReady
	c.snapshotClassSynced = alwaysReady
	c.recorder = &record.FakeRecorder{}

	for _, nls := range f.nlsLister {
		if err := i.Csi().V1alpha1().NodeLocalStorages().Informer().GetIndexer().Add(nls); err != nil {
			f.t.Fatalf("add nls %s to indexer failed", nls.Name)
		}
	}

	for _, nlsc := range f.nlscLister {
		if err := i.Csi().V1alpha1().NodeLocalStorageInitConfigs().Informer().GetIndexer().Add(nlsc); err != nil {
			f.t.Fatalf("add nlsc %s to indexer failed", nlsc.Name)
		}
	}

	for _, n := range f.nodeLister {
		if err := k8sI.Core().V1().Nodes().Informer().GetIndexer().Add(n); err != nil {
			f.t.Fatalf("add node %s to indexer failed", n.Name)
		}
	}

	return c, i, k8sI
}

func (f *fixture) run(nlscName string) {
	f.runController(nlscName, true, false)
}

func (f *fixture) runController(nlscName string, startInformers bool, expectError bool) {
	c, i, k8sI := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
		k8sI.Start(stopCh)
	}

	err := c.syncHandler(SyncNLSItem{
		nlscName: nlscName,
		nlsName:  "",
	})
	if !expectError && err != nil {
		f.t.Errorf("error syncing nlsc: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing nlsc, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.localactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.localactions), actions[i:])
			break
		}

		expectedAction := f.localactions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.localactions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.localactions)-len(actions), f.localactions[len(actions):])
	}

	k8sActions := filterInformerActions(f.kubeclient.Actions())
	for i, action := range k8sActions {
		if len(f.kubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(k8sActions)-len(f.kubeactions), k8sActions[i:])
			break
		}

		expectedAction := f.kubeactions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.kubeactions) > len(k8sActions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.kubeactions)-len(k8sActions), f.kubeactions[len(k8sActions):])
	}
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateActionImpl:
		e, _ := expected.(core.CreateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.UpdateActionImpl:
		e, _ := expected.(core.UpdateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.PatchActionImpl:
		e, _ := expected.(core.PatchActionImpl)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !reflect.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expPatch, patch))
		}
	default:
		t.Errorf("Uncaptured Action %s %s, you should explicitly add a case to capture it",
			actual.GetVerb(), actual.GetResource().Resource)
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "nodelocalstorages") ||
				action.Matches("watch", "nodelocalstorages") ||
				action.Matches("list", "nodelocalstorageinitconfigs") ||
				action.Matches("watch", "nodelocalstorageinitconfigs") ||
				action.Matches("list", "nodes") ||
				action.Matches("watch", "nodes")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectCreateNLSAction(nls *localv1alpha1.NodeLocalStorage) {
	f.localactions = append(
		f.localactions,
		core.NewCreateAction(schema.GroupVersionResource{Resource: "nodelocalstorages"}, "", nls),
	)
}

func (f *fixture) expectUpdateNLSAction(nls *localv1alpha1.NodeLocalStorage) {
	f.localactions = append(
		f.localactions,
		core.NewUpdateAction(schema.GroupVersionResource{Resource: "nodelocalstorages"}, "", nls),
	)
}

func TestCreateNLS(t *testing.T) {
	f := newFixture(t)
	nlsc := newNLSC("open-local")
	node_master := newMasterNode("master-0")
	nls_expected := newNLS("master-0")
	nls_expected.Spec.ListConfig = localv1alpha1.ListConfig{
		VGs: localv1alpha1.VGList{
			Include: []string{"open-local-pool-1"},
		},
	}

	f.nlscLister = append(f.nlscLister, nlsc)
	f.nodeLister = append(f.nodeLister, node_master)
	f.localobjects = append(f.localobjects, nlsc)
	f.kubeobjects = append(f.kubeobjects, node_master)

	f.expectCreateNLSAction(nls_expected)
	f.run(nlsc.Name)
}

func TestUpdateNLS(t *testing.T) {
	f := newFixture(t)
	nlsc := newNLSC("open-local")
	node_master := newMasterNode("master-0")
	nls := newNLS("master-0")
	nls.Spec.ListConfig = localv1alpha1.ListConfig{
		VGs: localv1alpha1.VGList{
			Include: []string{"open-local-pool-4"},
		},
	}
	nls_expected := nls.DeepCopy()
	nls_expected.Spec.ListConfig = localv1alpha1.ListConfig{
		VGs: localv1alpha1.VGList{
			Include: []string{"open-local-pool-1"},
		},
	}

	f.nlsLister = append(f.nlsLister, nls)
	f.nlscLister = append(f.nlscLister, nlsc)
	f.nodeLister = append(f.nodeLister, node_master)
	f.localobjects = append(f.localobjects, nlsc, nls)
	f.kubeobjects = append(f.kubeobjects, node_master)

	f.expectUpdateNLSAction(nls_expected)
	f.run(nlsc.Name)
}

func Test_SyncPVByNlsItem(t *testing.T) {

	nls := newNLSForAllocate(utils.NodeName3)

	pvcPVInfos := utils.TestPVCPVInfoList{
		utils.GetTestPVCPVWithVG(),
		utils.GetTestPVCPVDevice(),
	}
	pvcs := utils.CreateTestPersistentVolumeClaim(pvcPVInfos.GetTestPVCBounding())

	pvsHaveAllocateInfo := utils.CreateTestPersistentVolume(pvcPVInfos.GetTestPVBounding())

	lvmPVInfoWithoutVG := utils.GetTestPVCPVWithVG().PVBounding
	lvmPVInfoWithoutVG.VgName = ""
	devicePVInfoWithoutDeviceName := utils.GetTestPVCPVDevice().PVBounding
	devicePVInfoWithoutDeviceName.DeviceName = ""
	pvsWithoutAllocateInfo := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*lvmPVInfoWithoutVG, *devicePVInfoWithoutDeviceName})

	type args struct {
		oldNls *localv1alpha1.NodeLocalStorage
		newNls *localv1alpha1.NodeLocalStorage
	}

	type fields struct {
		pvs []*corev1.PersistentVolume
	}

	type expect struct {
		skipUpdate bool
		pvs        []*corev1.PersistentVolume
	}

	tests := []struct {
		name   string
		args   args
		fields fields
		expect expect
	}{
		{
			name: "test add nls, pv have no vg info",
			args: args{
				newNls: nls,
			},
			fields: fields{
				pvs: pvsWithoutAllocateInfo,
			},
			expect: expect{
				skipUpdate: false,
				pvs:        pvsHaveAllocateInfo,
			},
		},
		{
			name: "test update nls, but allocateInfo not change then skip",
			args: args{
				oldNls: nls,
				newNls: nls,
			},
			fields: fields{
				pvs: pvsWithoutAllocateInfo,
			},
			expect: expect{
				skipUpdate: true,
				pvs:        pvsWithoutAllocateInfo,
			},
		},
		{
			name: "test add nls, all pv have allocateInfo",
			args: args{
				newNls: nls,
			},
			fields: fields{
				pvs: pvsHaveAllocateInfo,
			},
			expect: expect{
				skipUpdate: false,
				pvs:        pvsHaveAllocateInfo,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			f := newFixture(t)
			c, localInforms, k8sInformers := f.newController()
			ctx := context.Background()
			localInforms.Start(ctx.Done())
			k8sInformers.Start(ctx.Done())
			f.client.CsiV1alpha1().NodeLocalStorages().Create(ctx, nls, metav1.CreateOptions{})
			localInforms.Csi().V1alpha1().NodeLocalStorages().Informer().GetIndexer().Add(nls)

			for _, pv := range tt.fields.pvs {
				f.kubeclient.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
				k8sInformers.Core().V1().PersistentVolumes().Informer().GetIndexer().Add(pv)
			}

			for _, pvc := range pvcs {
				f.kubeclient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
				k8sInformers.Core().V1().PersistentVolumeClaims().Informer().GetIndexer().Add(pvc)
			}

			gotSkip := c.enqueueSyncPVItemByNls(tt.args.oldNls, tt.args.newNls)
			assert.Equal(t, tt.expect.skipUpdate, gotSkip, "checkSkip")

			if gotSkip {
				return
			}
			c.processNextWorkItem()

			for _, expectPV := range tt.expect.pvs {
				gotPV, _ := f.kubeclient.CoreV1().PersistentVolumes().Get(ctx, expectPV.Name, metav1.GetOptions{})
				assert.Equal(t, expectPV, gotPV)
			}
		})
	}
}

func Test_enqueueAndHandlePVItem(t *testing.T) {
	nls := newNLSForAllocate(utils.NodeName3)

	pvcPVInfos := utils.TestPVCPVInfoList{
		utils.GetTestPVCPVWithVG(),
		utils.GetTestPVCPVDevice(),
	}
	pvcs := utils.CreateTestPersistentVolumeClaim(pvcPVInfos.GetTestPVCBounding())

	pvNotBoundInfo := utils.GetTestPVCPVWithVG().PVBounding
	pvNotBoundInfo.ClaimRef = nil
	pvNotBoundInfo.PVStatus = corev1.VolumeAvailable
	pvNotBound := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*pvNotBoundInfo})[0]

	lvmPVWithVG := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*utils.GetTestPVCPVWithVG().PVBounding})[0]

	lvmPVInfoWithoutVG := utils.GetTestPVCPVWithVG().PVBounding
	lvmPVInfoWithoutVG.VgName = ""
	lvmPVWithoutVG := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*lvmPVInfoWithoutVG})[0]

	devicePVWithDeviceName := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*utils.GetTestPVCPVDevice().PVBounding})[0]

	devicePVInfoWithoutDeviceName := utils.GetTestPVCPVDevice().PVBounding
	devicePVInfoWithoutDeviceName.DeviceName = ""
	devicePVWithoutDeviceName := utils.CreateTestPersistentVolume([]utils.TestPVInfo{*devicePVInfoWithoutDeviceName})[0]

	type args struct {
		pv *corev1.PersistentVolume
	}

	type expect struct {
		skipUpdate bool
		pv         *corev1.PersistentVolume
	}

	tests := []struct {
		name   string
		args   args
		expect expect
	}{
		{
			name: "test pv not bound",
			args: args{
				pv: pvNotBound,
			},
			expect: expect{
				skipUpdate: true,
			},
		},
		{
			name: "test lvm pv without vgName",
			args: args{
				pv: lvmPVWithoutVG,
			},
			expect: expect{
				skipUpdate: false,
				pv:         lvmPVWithVG,
			},
		},
		{
			name: "test lvm pv with vgName",
			args: args{
				pv: lvmPVWithVG,
			},
			expect: expect{
				skipUpdate: true,
			},
		},
		{
			name: "test device pv without deviceName",
			args: args{
				pv: devicePVWithoutDeviceName,
			},
			expect: expect{
				skipUpdate: false,
				pv:         devicePVWithDeviceName,
			},
		},
		{
			name: "test device pv with deviceName",
			args: args{
				pv: devicePVWithDeviceName,
			},
			expect: expect{
				skipUpdate: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			c, localInforms, k8sInformers := f.newController()
			ctx := context.Background()
			localInforms.Start(ctx.Done())
			k8sInformers.Start(ctx.Done())
			f.client.CsiV1alpha1().NodeLocalStorages().Create(ctx, nls, metav1.CreateOptions{})
			localInforms.Csi().V1alpha1().NodeLocalStorages().Informer().GetIndexer().Add(nls)

			f.kubeclient.CoreV1().PersistentVolumes().Create(ctx, tt.args.pv, metav1.CreateOptions{})
			k8sInformers.Core().V1().PersistentVolumes().Informer().GetIndexer().Add(tt.args.pv)

			for _, pvc := range pvcs {
				f.kubeclient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
				k8sInformers.Core().V1().PersistentVolumeClaims().Informer().GetIndexer().Add(pvc)
			}

			gotSkip := c.enqueuePVItem(tt.args.pv)
			assert.Equal(t, tt.expect.skipUpdate, gotSkip, "checkSkip")

			if gotSkip {
				return
			}
			c.processNextWorkItem()

			gotPV, _ := f.kubeclient.CoreV1().PersistentVolumes().Get(ctx, tt.expect.pv.Name, metav1.GetOptions{})
			assert.Equal(t, tt.expect.pv, gotPV)
		})
	}
}

func Test_deletePVC(t *testing.T) {
	nls := newNLSForAllocate(utils.NodeName3)

	pvcLVM := utils.CreateTestPersistentVolumeClaim([]utils.TestPVCInfo{*utils.GetTestPVCPVWithVG().PVCBounding})[0]
	nlsRemoveByPVCLVM := nls.DeepCopy()
	allocateInfos := &pkg.NodeStorageAllocateInfo{
		PvcAllocates: map[string]pkg.NodeStoragePVCAllocateInfo{
			utils.GetPVCKey(utils.LocalNameSpace, utils.PVCWithDevice): {
				PVCNameSpace: utils.LocalNameSpace,
				PVCName:      utils.PVCWithDevice,
				PVAllocatedInfo: pkg.PVAllocatedInfo{
					DeviceName: "/dev/sdc",
					VolumeType: string(pkg.VolumeTypeDevice),
				},
			},
		},
	}
	allocateInfoJson, _ := json.Marshal(allocateInfos)
	nlsRemoveByPVCLVM.Annotations[pkg.AnnotationNodeStorageAllocatedInfoKey] = string(allocateInfoJson)

	pvcDevice := utils.CreateTestPersistentVolumeClaim([]utils.TestPVCInfo{*utils.GetTestPVCPVDevice().PVCBounding})[0]
	nlsRemoveByPVCDevice := nls.DeepCopy()
	allocateInfos = &pkg.NodeStorageAllocateInfo{
		PvcAllocates: map[string]pkg.NodeStoragePVCAllocateInfo{
			utils.GetPVCKey(utils.LocalNameSpace, utils.PVCWithVG): {
				PVCNameSpace: utils.LocalNameSpace,
				PVCName:      utils.PVCWithVG,
				PVAllocatedInfo: pkg.PVAllocatedInfo{
					VGName:     utils.VGSSD,
					VolumeType: string(pkg.VolumeTypeLVM),
				},
			},
		},
	}
	allocateInfoJson, _ = json.Marshal(allocateInfos)
	nlsRemoveByPVCDevice.Annotations[pkg.AnnotationNodeStorageAllocatedInfoKey] = string(allocateInfoJson)

	type args struct {
		pvc *corev1.PersistentVolumeClaim
	}

	type expect struct {
		nls *localv1alpha1.NodeLocalStorage
	}

	tests := []struct {
		name   string
		args   args
		expect expect
	}{
		{
			name: "test lvm pvc delete",
			args: args{
				pvc: pvcLVM,
			},
			expect: expect{
				nls: nlsRemoveByPVCLVM,
			},
		},
		{
			name: "test lvm pvc delete",
			args: args{
				pvc: pvcDevice,
			},
			expect: expect{
				nls: nlsRemoveByPVCDevice,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			c, localInforms, k8sInformers := f.newController()
			ctx := context.Background()
			localInforms.Start(ctx.Done())
			k8sInformers.Start(ctx.Done())
			f.client.CsiV1alpha1().NodeLocalStorages().Create(ctx, nls, metav1.CreateOptions{})
			localInforms.Csi().V1alpha1().NodeLocalStorages().Informer().GetIndexer().Add(nls)

			c.deletePVC(tt.args.pvc)
			c.processNextWorkItem()

			gotNls, _ := f.client.CsiV1alpha1().NodeLocalStorages().Get(ctx, tt.expect.nls.Name, metav1.GetOptions{})
			assert.Equal(t, tt.expect.nls, gotNls)
		})
	}
}
