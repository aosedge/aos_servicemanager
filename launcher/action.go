// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package launcher

import (
	"container/list"
	"sync"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	maxExecutedActions = 10 // max number of actions processed simultaneously
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type actionHandler struct {
	sync.Mutex

	waitQueue *list.List
	workQueue *list.List
}

type serviceAction struct {
	id       string
	data     interface{}
	doAction func(string, interface{})
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newActionHandler() (handler *actionHandler, err error) {
	handler = &actionHandler{}

	handler.waitQueue = list.New()
	handler.workQueue = list.New()

	return handler, nil
}

func (handler *actionHandler) PutInQueue(action serviceAction) {
	handler.Lock()
	defer handler.Unlock()

	if handler.isIDInWorkQueue(action.id) {
		handler.waitQueue.PushBack(action)
		return
	}

	if handler.workQueue.Len() >= maxExecutedActions {
		handler.waitQueue.PushBack(action)
		return
	}

	go handler.processAction(handler.workQueue.PushBack(action))
}

func (handler *actionHandler) isIDInWorkQueue(id string) (result bool) {
	for item := handler.workQueue.Front(); item != nil; item = item.Next() {
		if item.Value.(serviceAction).id == id {
			return true
		}
	}

	return false
}

func (handler *actionHandler) processAction(item *list.Element) {
	action := item.Value.(serviceAction)

	action.doAction(action.id, action.data)

	handler.Lock()
	defer handler.Unlock()

	handler.workQueue.Remove(item)

	for item := handler.waitQueue.Front(); item != nil; item = item.Next() {
		if handler.isIDInWorkQueue(item.Value.(serviceAction).id) {
			continue
		}

		go handler.processAction(handler.workQueue.PushBack(handler.waitQueue.Remove(item)))
		break
	}
}
