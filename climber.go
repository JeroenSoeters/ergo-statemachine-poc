package main

import (
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

type StateCallback[T any] struct {
	state    gen.Atom
	message  reflect.Type
	callback func(actor T, from gen.PID, message any) error
}

type StateMachineBehavior[T any] interface {
	gen.ProcessBehavior

	Init(args ...any) (StateMachineSpec[T], error)

	HandleMessage(from gen.PID, message any) error

	HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)

	HandleEvent(event gen.MessageEvent) error

	HandleInspect(from gen.PID, item ...string) map[string]string

	//TODO: HandleCall, HandleTerminate

	State() gen.Atom
}

type StateMachine[T any] struct {
	gen.Process

	behavior StateMachineBehavior[T]
	mailbox  gen.ProcessMailbox

	spec StateMachineSpec[T]
}

type StateMachineSpec[T any] struct {
	initialState   gen.Atom
	stateCallbacks []StateCallback[T]
}

// ProcessBehavior implementation

func (sm *StateMachine[T]) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	fmt.Printf("In ProcessInit... %v\n", process.Behavior())

	if sm.behavior, ok = process.Behavior().(StateMachineBehavior[T]); ok == false {
		fmt.Printf("error with behavior %v\n", process.Behavior())
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

	fmt.Printf("Got spec: %v\n", spec)

	// set up callbacks

	return nil
}

func (sm *StateMachine[T]) ProcessRuAn() (rr error) {
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
			//  check if there is a registered handler, otherwise error
			if reason := sm.behavior.HandleMessage(message.From, message.Message); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result any

			// check if there is a registered handler, otherwise error
			result, reason = sm.behavior.HandleCall(message.From, message.Ref, message.Message)

			if reason != nil {
				// if reason is "normal" and we got response - send it before termination
				if reason == gen.TerminateReasonNormal && result != nil {
					sm.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}

			if result == nil {
				// async handling of sync request. response could be sent
				// later, even by the other process
				continue
			}

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

// client

type Climber struct {
	StateMachine[*Climber]
}

func factoryClimber() gen.ProcessBehavior {
	return &Climber{}
}

func (c *Climber) Init(args ...any) (StateMachineSpec[*Climber], error) {
	fmt.Println("In climber init")
	spec := StateMachineSpec[*Climber]{
		initialState:   gen.Atom("ready"),
		stateCallbacks: []StateCallback[*Climber]{},
	}

	c.Log().Info("Climber started in %s state", c.State())

	return spec, nil
}

// func (c *Climber) HandleMessage(from gen.PID, message any) error {
//  switch m := message.(type) {
//  case Belay:
//      c.Log().Info("Got ya!")
//      c.state = gen.Atom("on_belay")
//      return nil
//  case Climbing:
//      c.Log().Info("Go for it!")
//      c.state = gen.Atom("climb_on")
//      return nil
//  case Climb:
//      c.Log().Info("Be careful...")
//      m.f()
//      c.Log().Info("Great climb!")
//      c.state = gen.Atom("ready")
//  }
//
//	return nil
//}

type Belay struct{}

type Climbing struct{}

type Climb struct {
	f func()
}

func belay(c *Climber, from gen.PID, message any) error {
	c.Log().Info("Got ya!")
	return nil
}

func main() {
	fmt.Println("Hello, Ergo!")

	var opts gen.NodeOptions
	node, err := ergo.StartNode("climber@localhost", opts)

	if err != nil {
		panic(err)
	}

	fmt.Println("Starting climber")
	_, err = node.SpawnRegister("climber", factoryClimber, gen.ProcessOptions{})

	fmt.Println("Climber started")
	//	node.Send(gen.Atom("climber"), Belay{})
	//	node.Send(gen.Atom("climber"), Climbing{})
	//	node.Send(gen.Atom("climber"), Climb{f: func() { fmt.Println("Weeeeeee, I'm climbing!") }})

	node.Wait()

	fmt.Println("Exiting...")
}
