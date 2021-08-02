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

package action

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

// Handler action handler
type Handler struct {
	sync.Mutex

	waitQueue *list.List
	workQueue *list.List
}

type action struct {
	id       string
	data     interface{}
	doAction func(string, interface{})
}

/*******************************************************************************
 * Public
 ******************************************************************************/

func New() (handler *Handler, err error) {
	handler = &Handler{
		workQueue: list.New(),
		waitQueue: list.New(),
	}

	return handler, nil
}

func (handler *Handler) PutInQueue(id string, data interface{}, doAction func(string, interface{})) {
	handler.Lock()
	defer handler.Unlock()

	newAction := action{
		id:       id,
		data:     data,
		doAction: doAction,
	}

	if handler.isIDInWorkQueue(newAction.id) {
		handler.waitQueue.PushBack(newAction)
		return
	}

	if handler.workQueue.Len() >= maxExecutedActions {
		handler.waitQueue.PushBack(newAction)
		return
	}

	go handler.processAction(handler.workQueue.PushBack(newAction))
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (handler *Handler) isIDInWorkQueue(id string) (result bool) {
	for item := handler.workQueue.Front(); item != nil; item = item.Next() {
		if item.Value.(action).id == id {
			return true
		}
	}

	return false
}

func (handler *Handler) processAction(item *list.Element) {
	currentAction := item.Value.(action)

	currentAction.doAction(currentAction.id, currentAction.data)

	handler.Lock()
	defer handler.Unlock()

	handler.workQueue.Remove(item)

	for item := handler.waitQueue.Front(); item != nil; item = item.Next() {
		if handler.isIDInWorkQueue(item.Value.(action).id) {
			continue
		}

		go handler.processAction(handler.workQueue.PushBack(handler.waitQueue.Remove(item)))

		break
	}
}
