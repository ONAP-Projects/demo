/*
Copyright 2019 Intel Corporation.
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

package utils

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/go-logr/logr"

	onapv1alpha1 "collectd-operator/pkg/apis/onap/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Define the collectdPlugin finalizer for handling deletion
const (
	defaultWatchLabel = "app=collectd"
	CollectdFinalizer = "finalizer.collectd.onap.org"

	// WatchLabelsEnvVar is the constant for env variable WATCH_LABELS
	// which is the labels where the watch activity happens.
	// this value is empty if the operator is running with clusterScope.
	WatchLabelsEnvVar = "WATCH_LABELS"
)

var lock sync.Mutex

// ReconcileLock - Used to sync between global and plugin controller
var ReconcileLock sync.Mutex

// ResourceMap to hold objects to update/reload
type ResourceMap struct {
	ConfigMap       *corev1.ConfigMap
	DaemonSet       *appsv1.DaemonSet
	CollectdPlugins *[]onapv1alpha1.CollectdPlugin
}

// ComputeSHA256  returns hash of data as string
func ComputeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

// Contains checks if a string is contained in a list of strings
func Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Remove removes a string from a list of string
func Remove(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// GetWatchLabels returns the labels the operator should be watching for changes
func GetWatchLabels() (string, error) {
	labelSelector, found := os.LookupEnv(WatchLabelsEnvVar)
	if !found {
		return defaultWatchLabel, fmt.Errorf("%s must be set", WatchLabelsEnvVar)
	}
	return labelSelector, nil
}

// GetCollectdPluginList returns the list of CollectdPlugin instances in the namespace ns
func GetCollectdPluginList(rc client.Client, ns string) (*onapv1alpha1.CollectdPluginList, error) {
	// Get all collectd plugins in the current namespace to rebuild conf.
	collectdPlugins := &onapv1alpha1.CollectdPluginList{}
	cpOpts := &client.ListOptions{}
	cpOpts.InNamespace(ns)
	err := rc.List(context.TODO(), cpOpts, collectdPlugins)
	if err != nil {
		return nil, err
	}
	return collectdPlugins, nil
}

// GetConfigMap returns the GetConfigMap in the namespace ns
func GetConfigMap(rc client.Client, reqLogger logr.Logger, ns string) (*corev1.ConfigMap, error) {
	lock.Lock()
	defer lock.Unlock()

	reqLogger.Info("Get ConfigMap for collectd.conf")
	// Get all collectd plugins in the current namespace to rebuild conf.
	cmList := &corev1.ConfigMapList{}
	opts := &client.ListOptions{}
	// Select ConfigMaps with label
	labelSelector, err := GetWatchLabels()
	if err != nil {
		reqLogger.Error(err, "Failed to get watch labels, continuing with default label")
	}
	opts.SetLabelSelector(labelSelector)
	opts.InNamespace(ns)

	err = rc.List(context.TODO(), opts, cmList)
	if err != nil {
		return nil, err
	}

	if cmList.Items == nil || len(cmList.Items) == 0 {
		return nil, errors.NewNotFound(corev1.Resource("configmap"), "ConfigMap")
	}

	cm := &cmList.Items[0]
	return cm, nil
}

// GetCollectdGlobal returns the CollectdGlobal instance in the namespace ns
func GetCollectdGlobal(rc client.Client, ns string) (*onapv1alpha1.CollectdGlobal, error) {
	// Get the CollectdGlobal instance in current namespace to rebuild conf.
	cgList := &onapv1alpha1.CollectdGlobalList{}
	cpOpts := &client.ListOptions{}
	cpOpts.InNamespace(ns)
	err := rc.List(context.TODO(), cpOpts, cgList)
	if err != nil {
		return nil, err
	}
	if cgList.Items == nil || len(cgList.Items) == 0 {
		return nil, err
	}
	collectdGlobals := &cgList.Items[0]
	return collectdGlobals, nil
}

// GetPodList returns the list of pods in the namespace ns
func GetPodList(rc client.Client, ns string) ([]string, error) {
	var pods []string
	podList := &corev1.PodList{}
	opts := &client.ListOptions{}
	// Select ConfigMaps with label
	labelSelector, _ := GetWatchLabels()
	opts.SetLabelSelector(labelSelector)
	opts.InNamespace(ns)
	err := rc.List(context.TODO(), opts, podList)
	if err != nil {
		return nil, err
	}

	if podList.Items == nil || len(podList.Items) == 0 {
		return nil, err
	}

	for _, pod := range podList.Items {
		pods = append(pods, pod.Name)
	}
	return pods, nil
}

// RebuildCollectdConf Get all collectd plugins and reconstruct, compute Hash and check for changes
func RebuildCollectdConf(rc client.Client, ns string, isDelete bool, delPlugin string) (string, error) {
	var collectdConf, collectdGlobalConf string
	// Get the collectd global in the current namespace to rebuild conf.
	cg, err := GetCollectdGlobal(rc, ns)
	if err != nil {
		return "", err
	}

	if cg != nil {
		collectdGlobalConf += cg.Spec.GlobalOptions + "\n"
	}

	// Get all collectd plugins in the current namespace to rebuild conf.
	cp, err := GetCollectdPluginList(rc, ns)
	if err != nil {
		return "", err
	}
	cpList := &cp.Items
	loadPlugin := make(map[string]string)
	if *cpList != nil && len(*cpList) > 0 {
		for _, cp := range *cpList {
			// using CollectdPlugin to set global options. If CollectdGlobal CRD is defined do not check for this
			if cp.Spec.PluginName == "global" {
				if cg == nil {
					collectdGlobalConf += cp.Spec.PluginConf + "\n"
				}
			} else {
				loadPlugin[cp.Spec.PluginName] = cp.Spec.PluginConf
			}
		}
	}

	if isDelete {
		delete(loadPlugin, delPlugin)
	} else {
		collectdConf += collectdGlobalConf
	}

	pluginKeys := make([]string, 0, len(loadPlugin))
	for k := range loadPlugin {
		pluginKeys = append(pluginKeys, k)
	}
	sort.Strings(pluginKeys)

	for _, cpName := range pluginKeys {
		cpConf := loadPlugin[cpName]
		collectdConf += "LoadPlugin" + " " + cpName + "\n"
		collectdConf += cpConf + "\n"
	}

	collectdConf += "#Last line (collectd requires '\\n' at the last line)\n"

	return collectdConf, nil
}
