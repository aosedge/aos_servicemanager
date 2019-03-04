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
	mutex sync.Mutex

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
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

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

	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	handler.workQueue.Remove(item)

	for item := handler.waitQueue.Front(); item != nil; item = item.Next() {
		if handler.isIDInWorkQueue(item.Value.(serviceAction).id) {
			continue
		}

		go handler.processAction(handler.workQueue.PushBack(handler.waitQueue.Remove(item)))
		break
	}
}
