/*
Copyright 2021 The KodeRover Authors.

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

package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/koderover/zadig/pkg/microservice/aslan/config"
	commonmodels "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/models"
	commonrepo "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb"
	"github.com/koderover/zadig/pkg/setting"
	kubeclient "github.com/koderover/zadig/pkg/shared/kube/client"
	"github.com/koderover/zadig/pkg/shared/kube/wrapper"
	e "github.com/koderover/zadig/pkg/tool/errors"
	"github.com/koderover/zadig/pkg/tool/kube/getter"
	"github.com/koderover/zadig/pkg/tool/kube/podexec"
	"github.com/koderover/zadig/pkg/types"
)

var cleanCacheLock sync.Mutex

const (
	CleanStatusUnStart  = "unStart"
	CleanStatusSuccess  = "success"
	CleanStatusCleaning = "cleaning"
	CleanStatusFailed   = "failed"
)

// SetCron set the docker clean cron
func SetCron(cron string, cronEnabled bool, logger *zap.SugaredLogger) error {
	dindCleans, err := commonrepo.NewDindCleanColl().List()
	if err != nil {
		logger.Errorf("list dind cleans err:%s", err)
		return err
	}
	switch len(dindCleans) {
	case 0:
		dindClean := &commonmodels.DindClean{
			Status:         CleanStatusUnStart,
			DindCleanInfos: []*commonmodels.DindCleanInfo{},
			Cron:           cron,
			CronEnabled:    cronEnabled,
		}

		if err := commonrepo.NewDindCleanColl().Upsert(dindClean); err != nil {
			return e.ErrCreateDindClean.AddErr(err)
		}
	case 1:
		dindClean := dindCleans[0]
		dindClean.Status = CleanStatusSuccess
		dindClean.DindCleanInfos = dindCleans[0].DindCleanInfos
		dindClean.Cron = cron
		dindClean.CronEnabled = cronEnabled
		if err := commonrepo.NewDindCleanColl().Upsert(dindClean); err != nil {
			return e.ErrUpdateDindClean.AddErr(err)
		}
	}
	return nil
}

func CleanImageCache(logger *zap.SugaredLogger) error {
	// TODO: We should return immediately instead of waiting for the lock if a cleanup task is performed.
	//       Since `golang-1.18`, `sync.Mutex` provides a `TryLock()` method. For now, we can continue with the previous
	//       logic and replace it with `TryLock()` after upgrading to `golang-1.18+` to return immediately.
	cleanCacheLock.Lock()
	defer cleanCacheLock.Unlock()

	dindCleans, err := commonrepo.NewDindCleanColl().List()
	if err != nil {
		return fmt.Errorf("failed to list data in `dind_clean` table: %s", err)
	}

	switch len(dindCleans) {
	case 0:
		dindClean := &commonmodels.DindClean{
			Status:         CleanStatusCleaning,
			DindCleanInfos: []*commonmodels.DindCleanInfo{},
		}

		if err := commonrepo.NewDindCleanColl().Upsert(dindClean); err != nil {
			return e.ErrCreateDindClean.AddErr(err)
		}
	case 1:
		dindClean := dindCleans[0]
		if dindClean.Status == CleanStatusCleaning {
			return e.ErrDindClean.AddDesc("")
		}
		dindClean.Status = CleanStatusCleaning
		dindClean.DindCleanInfos = []*commonmodels.DindCleanInfo{}
		dindClean.Cron = dindCleans[0].Cron
		dindClean.CronEnabled = dindCleans[0].CronEnabled
		if err := commonrepo.NewDindCleanColl().Upsert(dindClean); err != nil {
			return e.ErrUpdateDindClean.AddErr(err)
		}
	}

	dindPods, failedClusters, err := getDindPods(logger)
	if err != nil {
		logger.Errorf("Failed to list dind pods: %s", err)
		errInfo := &commonmodels.DindCleanInfo{
			PodName:      "system",
			StartTime:    time.Now().Unix(),
			EndTime:      time.Now().Unix(),
			ErrorMessage: fmt.Sprintf("Failed to list dind pods: %s", err),
		}
		return commonrepo.NewDindCleanColl().Upsert(&commonmodels.DindClean{
			Status:         CleanStatusFailed,
			DindCleanInfos: []*commonmodels.DindCleanInfo{errInfo},
			Cron:           dindCleans[0].Cron,
			CronEnabled:    dindCleans[0].CronEnabled,
		})
	}
	logger.Infof("Total dind Pods found: %d", len(dindPods))
	if len(failedClusters) > 0 {
		logger.Warnf("Some clusters were inaccessible and will be skipped: %s", strings.Join(failedClusters, "; "))
	}

	// Note: Since the total number of dind instances of Zadig users will not exceed `50` within one or two years
	// (at this time, the resource amount may be `200C400GiB`, and the resource cost is too high), concurrency can be
	// left out of consideration.
	timeout, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res := make(chan *commonmodels.DindClean)
	go func(ch chan *commonmodels.DindClean, failedClustersInfo []string) {
		var (
			status            = CleanStatusSuccess
			dindInfosMutex    sync.Mutex
			readyPodsCount    = 0
			successPodsCount  = 0
			notReadyPods      []string
			hasCleanupAttempt = false
		)

		dindInfos := make([]*commonmodels.DindCleanInfo, 0, len(dindPods)+len(failedClustersInfo))
		var wg sync.WaitGroup

		// Add failed clusters as warning entries in the cleanup results
		for _, failedCluster := range failedClustersInfo {
			dindInfos = append(dindInfos, &commonmodels.DindCleanInfo{
				PodName:      fmt.Sprintf("cluster-error: %s", failedCluster),
				StartTime:    time.Now().Unix(),
				EndTime:      time.Now().Unix(),
				CleanInfo:    "Cluster was inaccessible and skipped",
				ErrorMessage: fmt.Sprintf("Failed to access cluster: %s", failedCluster),
			})
		}

		// Check and log pod readiness status
		for _, dindPod := range dindPods {
			if !wrapper.Pod(dindPod.Pod).Ready() {
				notReadyPods = append(notReadyPods, fmt.Sprintf("%s:%s (status: %s)", dindPod.ClusterName, dindPod.Pod.Name, dindPod.Pod.Status.Phase))
				logger.Warnf("Dind pod %q in ns %q of cluster %q is not ready (phase: %s), skipping", dindPod.Pod.Name, dindPod.Pod.Namespace, dindPod.ClusterID, dindPod.Pod.Status.Phase)
				continue
			}
			readyPodsCount++
			hasCleanupAttempt = true

			logger.Infof("Begin to clean up cache of dind %q in ns %q of cluster %q.", dindPod.Pod.Name, dindPod.Pod.Namespace, dindPod.ClusterID)
			wg.Add(1)
			go func(podInfo types.DindPod) {
				defer wg.Done()
				dindInfo := &commonmodels.DindCleanInfo{
					PodName:   fmt.Sprintf("%s:%s", podInfo.ClusterName, podInfo.Pod.Name),
					StartTime: time.Now().Unix(),
				}
				output, err := dockerPrune(podInfo.ClusterID, podInfo.Pod.Namespace, podInfo.Pod.Name, logger)
				if err != nil {
					logger.Warnf("Failed to clean up cache of dind %q in ns %q of cluster %q: %s", podInfo.Pod.Name, podInfo.Pod.Namespace, podInfo.ClusterID, err)
					dindInfo.ErrorMessage = err.Error()
				} else {
					// Track successful cleanups
					dindInfosMutex.Lock()
					successPodsCount++
					dindInfosMutex.Unlock()
				}
				logger.Infof("Finish cleaning up cache of dind %q in ns %q of cluster %q.", podInfo.Pod.Name, podInfo.Pod.Namespace, podInfo.ClusterID)

				dindInfo.EndTime = time.Now().Unix()
				dindInfo.CleanInfo = output

				// Thread-safe append
				dindInfosMutex.Lock()
				dindInfos = append(dindInfos, dindInfo)
				dindInfosMutex.Unlock()
			}(dindPod)
		}

		wg.Wait()

		// Determine final status based on cleanup results
		// Success if at least one pod was successfully cleaned, even if some clusters failed
		if !hasCleanupAttempt {
			// No pods were available for cleanup (all clusters failed or no ready pods)
			status = CleanStatusFailed
			errMsg := "No dind pods available for cleanup"
			if len(dindPods) == 0 {
				errMsg = fmt.Sprintf("No dind pods found in accessible clusters (%d clusters failed)", len(failedClustersInfo))
			} else if readyPodsCount == 0 {
				errMsg = fmt.Sprintf("No ready dind pods found. Total pods: %d, Not ready: %d", len(dindPods), len(notReadyPods))
				if len(notReadyPods) > 0 {
					errMsg += fmt.Sprintf(". Not ready pods: %s", strings.Join(notReadyPods, ", "))
				}
			}
			logger.Errorf(errMsg)

			dindInfos = append(dindInfos, &commonmodels.DindCleanInfo{
				PodName:      "system",
				StartTime:    time.Now().Unix(),
				EndTime:      time.Now().Unix(),
				ErrorMessage: errMsg,
			})
		} else if successPodsCount > 0 {
			// At least one pod was successfully cleaned - mark as success
			status = CleanStatusSuccess
			logger.Infof("Successfully cleaned cache for %d/%d dind pods", successPodsCount, readyPodsCount)
			if successPodsCount < readyPodsCount {
				logger.Warnf("%d pod(s) failed to clean", readyPodsCount-successPodsCount)
			}
		} else {
			// All cleanup attempts failed
			status = CleanStatusFailed
			logger.Errorf("All %d cleanup attempts failed", readyPodsCount)
		}

		res <- &commonmodels.DindClean{
			Status:         status,
			DindCleanInfos: dindInfos,
			Cron:           dindCleans[0].Cron,
			CronEnabled:    dindCleans[0].CronEnabled,
		}

	}(res, failedClusters)

	select {
	case <-timeout.Done():
		logger.Errorf("Dind cache cleanup operation timed out after 20 minutes")
		timeoutErrInfo := &commonmodels.DindCleanInfo{
			PodName:      "system",
			StartTime:    time.Now().Unix(),
			EndTime:      time.Now().Unix(),
			ErrorMessage: "Cache cleanup operation timed out after 20 minutes",
		}
		commonrepo.NewDindCleanColl().Upsert(&commonmodels.DindClean{
			Status:         CleanStatusFailed,
			DindCleanInfos: []*commonmodels.DindCleanInfo{timeoutErrInfo},
			Cron:           dindCleans[0].Cron,
			CronEnabled:    dindCleans[0].CronEnabled,
		})
	case info := <-res:
		commonrepo.NewDindCleanColl().Upsert(info)
	}

	return nil
}

// GetOrCreateCleanCacheState 获取清理镜像缓存状态，如果数据库中没有数据返回一个临时对象
func GetOrCreateCleanCacheState() (*commonmodels.DindClean, error) {
	var dindClean *commonmodels.DindClean
	dindCleans, _ := commonrepo.NewDindCleanColl().List()
	if len(dindCleans) == 0 {
		dindClean = &commonmodels.DindClean{
			Status:         CleanStatusUnStart,
			DindCleanInfos: []*commonmodels.DindCleanInfo{},
			UpdateTime:     time.Now().Unix(),
		}
		return dindClean, nil
	}

	sort.SliceStable(dindCleans[0].DindCleanInfos, func(i, j int) bool {
		return dindCleans[0].DindCleanInfos[i].PodName < dindCleans[0].DindCleanInfos[j].PodName
	})

	dindClean = &commonmodels.DindClean{
		ID:             dindCleans[0].ID,
		Status:         dindCleans[0].Status,
		UpdateTime:     dindCleans[0].UpdateTime,
		DindCleanInfos: dindCleans[0].DindCleanInfos,
		Cron:           dindCleans[0].Cron,
		CronEnabled:    dindCleans[0].CronEnabled,
	}
	return dindClean, nil
}

func dockerPrune(clusterID, namespace, podName string, logger *zap.SugaredLogger) (string, error) {
	kclient, err := kubeclient.GetClientset(config.HubServerAddress(), clusterID)
	if err != nil {
		return "", fmt.Errorf("failed to get clientset for cluster %q: %s", clusterID, err)
	}

	restConfig, err := kubeclient.GetRESTConfig(config.HubServerAddress(), clusterID)
	if err != nil {
		return "", fmt.Errorf("failed to get rest config for cluster %q: %s", clusterID, err)
	}

	cleanInfo, errString, _, err := podexec.KubeExec(kclient, restConfig, podexec.ExecOptions{
		Command:   []string{"docker", "system", "prune", "--volumes", "-a", "-f"},
		Namespace: namespace,
		PodName:   podName,
	})
	if err != nil {
		logger.Errorf("Failed to execute docker prune: %s, err: %s", errString, err)
		cleanInfo = errString
	}

	cleanInfoArr := strings.Split(cleanInfo, "\n\n")
	if len(cleanInfoArr) >= 2 {
		cleanInfo = cleanInfoArr[1]
	}
	cleanInfo = strings.Replace(cleanInfo, "\n", "", -1)

	return cleanInfo, err
}

func getDindPods(logger *zap.SugaredLogger) ([]types.DindPod, []string, error) {
	activeClusters, err := commonrepo.NewK8SClusterColl().FindActiveClusters()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get active cluster: %s", err)
	}

	logger.Infof("Found %d active clusters for dind pod discovery", len(activeClusters))

	dindPods := []types.DindPod{}
	var failedClusters []string

	for _, cluster := range activeClusters {
		clusterID := cluster.ID.Hex()

		var ns string
		switch clusterID {
		case setting.LocalClusterID:
			ns = config.Namespace()
		default:
			ns = setting.AttachedClusterNamespace
		}

		logger.Infof("Searching for dind pods in cluster %q (ID: %s) namespace %q", cluster.Name, clusterID, ns)

		pods, err := getDindPodsInCluster(clusterID, ns, logger)
		if err != nil {
			// Log the error but continue with other clusters
			logger.Warnf("Failed to get dind pods in cluster %q (ID: %s): %s", cluster.Name, clusterID, err)
			failedClusters = append(failedClusters, fmt.Sprintf("%s (%s)", cluster.Name, err.Error()))
			continue
		}

		logger.Infof("Found %d dind pods in cluster %q namespace %q", len(pods), cluster.Name, ns)

		for _, pod := range pods {
			dindPods = append(dindPods, types.DindPod{
				ClusterID:   clusterID,
				ClusterName: cluster.Name,
				Pod:         pod,
			})
		}
	}

	// Only return error if we couldn't access ANY cluster
	if len(dindPods) == 0 && len(failedClusters) > 0 {
		return nil, failedClusters, fmt.Errorf("failed to access all clusters: %s", strings.Join(failedClusters, "; "))
	}

	if len(failedClusters) > 0 {
		logger.Warnf("Successfully accessed %d clusters, but %d clusters failed: %s",
			len(activeClusters)-len(failedClusters), len(failedClusters), strings.Join(failedClusters, "; "))
	}

	return dindPods, failedClusters, nil
}

func getDindPodsInCluster(clusterID, ns string, logger *zap.SugaredLogger) ([]*corev1.Pod, error) {
	kclient, err := kubeclient.GetKubeClient(config.HubServerAddress(), clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get kube client for cluster %q: %s", clusterID, err)
	}

	dindSelector := labels.Set{setting.ComponentLabel: "dind"}.AsSelector()
	logger.Infof("Looking for pods with label selector: %s=%s in namespace %q", setting.ComponentLabel, "dind", ns)

	pods, err := getter.ListPods(ns, dindSelector, kclient)
	if err != nil {
		return nil, err
	}

	if len(pods) == 0 {
		logger.Warnf("No dind pods found with label %s=dind in namespace %q of cluster %q", setting.ComponentLabel, ns, clusterID)
	}

	return pods, nil
}
