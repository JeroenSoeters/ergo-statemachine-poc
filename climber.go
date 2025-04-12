package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"ergo.services/ergo"
	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
)

// call/cast from message state data
// from message state

// framework

type StateMachineBehavior[T any] interface {
	gen.ProcessBehavior

	Init(args ...any) (StateMachineSpec[T], error)

	HandleMessage(from gen.PID, message any) error

	HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)

	HandleEvent(event gen.MessageEvent) error

	HandleInspect(from gen.PID, item ...string) map[string]string

	CurrentState() gen.Atom
}

type StateMachine[T any] struct {
	gen.Process

	behavior StateMachineBehavior[T]
	mailbox  gen.ProcessMailbox

	spec StateMachineSpec[T]

	currentState   gen.Atom
	stateCallbacks map[gen.Atom]map[string]StateCallback[T]
}

type StateMachineSpec[T any] struct {
	initialState   gen.Atom
	stateCallbacks map[gen.Atom]map[string]StateCallback[T]
}

// ProcessBehavior implementation

func (sm *StateMachine[T]) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if sm.behavior, ok = process.Behavior().(StateMachineBehavior[T]); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not a StateMachineBehavior %s", unknown)
	}

	sm.Process = process
	sm.mailbox = process.Mailbox()

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				sm.Log().Panic("StateMachine initialization failed. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	spec, err := sm.behavior.Init(args...)
	if err != nil {
		return err
	}

	// set up callbacks
	sm.currentState = spec.initialState
	sm.stateCallbacks = spec.stateCallbacks

	return nil
}

func (sm *StateMachine[T]) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				sm.Log().Panic("StateMachine terminated. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	for {
		if sm.State() != gen.ProcessStateRunning {
			// process was killed by the node.
			return gen.TerminateReasonKill
		}

		if message != nil {
			gen.ReleaseMailboxMessage(message)
			message = nil
		}

		for {
			// check queues
			msg, ok := sm.mailbox.Urgent.Pop()
			if ok {
				// got new urgent message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = sm.mailbox.System.Pop()
			if ok {
				// got new system message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = sm.mailbox.Main.Pop()
			if ok {
				// got new regular message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			if _, ok := sm.mailbox.Log.Pop(); ok {
				panic("statemachne process can not be a logger")
			}

			// no messages in the mailbox
			return nil
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			// check if there is a handler for the message in the current state
			typeName := typeName(message)
			if callback, ok := sm.lookupCallback(typeName); ok == true {
				return callback(sm, message.Message)
			}
			return fmt.Errorf("Unsupported message %s for state %s", typeName, sm.currentState)

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result gen.Atom = sm.CurrentState()

			// check if there is a handler for the call in the current state
			typeName := typeName(message)
			if callback, ok := sm.lookupCallback(typeName); ok == true {
				reason = callback(sm, message.Message)
				result = sm.CurrentState()
			} else {
				reason = fmt.Errorf("Unsupported call %s for state %s", typeName, sm.currentState)
			}

			if reason != nil {
				// if reason is "normal" and we got response - send it before termination
				if reason == gen.TerminateReasonNormal {
					sm.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}

			// Note: we do not support async handling of sync request at the moment

			sm.SendResponse(message.From, message.Ref, result)

		case gen.MailboxMessageTypeEvent:
			if reason := sm.behavior.HandleEvent(message.Message.(gen.MessageEvent)); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeExit:
			switch exit := message.Message.(type) {
			case gen.MessageExitPID:
				return fmt.Errorf("%s: %w", exit.PID, exit.Reason)

			case gen.MessageExitProcessID:
				return fmt.Errorf("%s: %w", exit.ProcessID, exit.Reason)

			case gen.MessageExitAlias:
				return fmt.Errorf("%s: %w", exit.Alias, exit.Reason)

			case gen.MessageExitEvent:
				return fmt.Errorf("%s: %w", exit.Event, exit.Reason)

			case gen.MessageExitNode:
				return fmt.Errorf("%s: %w", exit.Name, gen.ErrNoConnection)

			default:
				panic(fmt.Sprintf("unknown exit message: %#v", exit))
			}

		case gen.MailboxMessageTypeInspect:
			result := sm.behavior.HandleInspect(message.From, message.Message.([]string)...)
			sm.SendResponse(message.From, message.Ref, result)
		}

	}
}

func (sm *StateMachine[T]) ProcessTerminate(reason error) {
}

//
// StateMachineBehavior default callbacks
//

func (s *StateMachine[T]) HandleMessage(from gen.PID, message any) error {
	s.Log().Warning("StateMachine.HandleMessage: unhandled message from %s", from)
	return nil
}

func (s *StateMachine[T]) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	s.Log().Warning("StateMachine.HandleCall: unhandled request from %s", from)
	return nil, nil
}
func (s *StateMachine[T]) HandleEvent(message gen.MessageEvent) error {
	s.Log().Warning("StateMachine.HandleEvent: unhandled event message %#v", message)
	return nil
}

func (s *StateMachine[T]) HandleInspect(from gen.PID, item ...string) map[string]string {
	return nil
}

func (s *StateMachine[T]) Terminate(reason error) {}

func (s *StateMachine[T]) CurrentState() gen.Atom {
	return s.currentState
}

type StateCallback[T any] func(*StateMachine[T], any) error

type Callback[T any] func(*StateMachineSpec[T])

func NewStateMachineSpec[T any](initialState gen.Atom, callbacks ...Callback[T]) StateMachineSpec[T] {
	spec := StateMachineSpec[T]{
		initialState:   initialState,
		stateCallbacks: make(map[gen.Atom]map[string]StateCallback[T]),
	}
	for _, cb := range callbacks {
		cb(&spec)
	}
	return spec
}

func WithStateCallback[SpecType any, MessageType any](state gen.Atom, callback func(*StateMachine[SpecType], any) error) Callback[SpecType] {
	typeName := reflect.TypeOf((*MessageType)(nil)).Elem().String()
	return func(s *StateMachineSpec[SpecType]) {
		if _, exists := s.stateCallbacks[state]; exists == false {
			s.stateCallbacks[state] = make(map[string]StateCallback[SpecType])
		}
		s.stateCallbacks[state][typeName] = callback
	}
}

// internals

func typeName(message *gen.MailboxMessage) string {
	return reflect.TypeOf(message.Message).String()
}

func (sm *StateMachine[T]) lookupCallback(messageType string) (StateCallback[T], bool) {
	if stateCallbacks, exists := sm.stateCallbacks[sm.currentState]; exists == true {
		if callback, exists := stateCallbacks[messageType]; exists == true {
			return callback, true
		}
	}
	return nil, false
}

// client

type ClimbData struct {
}

type Climber struct {
	StateMachine[ClimbData]
}

func factoryClimber() gen.ProcessBehavior {
	return &Climber{}
}

func (c *Climber) Init(args ...any) (StateMachineSpec[ClimbData], error) {
	spec := NewStateMachineSpec(gen.Atom("ready"),
		WithStateCallback[ClimbData, Belay](gen.Atom("ready"), belay),
		WithStateCallback[ClimbData, Climbing](gen.Atom("on_belay"), climbing),
		WithStateCallback[ClimbData, Climb](gen.Atom("climb_on"), climb),
	)

	c.Log().Info("Climber started in %s state", c.State())

	return spec, nil
}

type Belay struct{}

type Climbing struct{}

type Climb struct {
	f func()
}

func belay(sm *StateMachine[ClimbData], message any) error {
	sm.Log().Info("Got ya!")
	sm.currentState = gen.Atom("on_belay")
	return nil
}

func climbing(sm *StateMachine[ClimbData], message any) error {
	sm.Log().Info("Go for it!")
	sm.currentState = gen.Atom("climb_on")
	return nil
}

func climb(sm *StateMachine[ClimbData], message any) error {
	var ok bool
	var m Climb

	if m, ok = message.(Climb); ok == false {
		return errors.New("we have a problem")
	}
	sm.Log().Info("Be careful...")
	m.f()
	sm.Log().Info("Great climb!")
	return nil
}

func main() {
	var opts gen.NodeOptions
	node, err := ergo.StartNode("climber@localhost", opts)

	if err != nil {
		panic(err)
	}

	var _ gen.Process = (*Climber)(nil)

	_, err = node.SpawnRegister("climber", factoryClimber, gen.ProcessOptions{})

	node.Send(gen.Atom("climber"), Belay{})
	//node.Send(gen.Atom("climber"), Belay{}) // this will throw an unsupported message for state exception
	node.Send(gen.Atom("climber"), Climbing{})
	node.Send(gen.Atom("climber"), Climb{f: func() { fmt.Println("Weeeeeee, I'm climbing!") }})

	node.Wait()
}
