// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2016 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package state_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	. "gopkg.in/check.v1"

	"github.com/ubuntu-core/snappy/overlord/state"
)

func TestState(t *testing.T) { TestingT(t) }

type stateSuite struct{}

var _ = Suite(&stateSuite{})

type mgrState1 struct {
	A string
}

type Count2 struct {
	B int
}

type mgrState2 struct {
	C *Count2
}

func (ss *stateSuite) TestLockUnlock(c *C) {
	st := state.New(nil)
	st.Lock()
	st.Unlock()
}

func (ss *stateSuite) TestGetAndSet(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	mSt1 := &mgrState1{A: "foo"}
	st.Set("mgr1", mSt1)
	mSt2 := &mgrState2{C: &Count2{B: 42}}
	st.Set("mgr2", mSt2)

	var mSt1B mgrState1
	err := st.Get("mgr1", &mSt1B)
	c.Assert(err, IsNil)
	c.Check(&mSt1B, DeepEquals, mSt1)

	var mSt2B mgrState2
	err = st.Get("mgr2", &mSt2B)
	c.Assert(err, IsNil)
	c.Check(&mSt2B, DeepEquals, mSt2)
}

func (ss *stateSuite) TestSetPanic(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	unsupported := struct {
		Ch chan bool
	}{}
	c.Check(func() { st.Set("mgr9", unsupported) }, PanicMatches, `internal error: could not marshal value for state entry "mgr9": json: unsupported type:.*`)
}

func (ss *stateSuite) TestGetNoState(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	var mSt1B mgrState1
	err := st.Get("mgr9", &mSt1B)
	c.Check(err, Equals, state.ErrNoState)
}

func (ss *stateSuite) TestGetUnmarshalProblem(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	mismatched := struct {
		A int
	}{A: 22}
	st.Set("mgr9", &mismatched)

	var mSt1B mgrState1
	err := st.Get("mgr9", &mSt1B)
	c.Check(err, ErrorMatches, `internal error: could not unmarshal state entry "mgr9": json: cannot unmarshal .*`)
}

func (ss *stateSuite) TestCache(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	type key1 struct{}
	type key2 struct{}

	c.Assert(st.Cached(key1{}), Equals, nil)

	st.Cache(key1{}, "value1")
	st.Cache(key2{}, "value2")
	c.Assert(st.Cached(key1{}), Equals, "value1")
	c.Assert(st.Cached(key2{}), Equals, "value2")

	st.Cache(key1{}, nil)
	c.Assert(st.Cached(key1{}), Equals, nil)

	_, ok := st.Cached("key3").(string)
	c.Assert(ok, Equals, false)
}

type fakeStateBackend struct {
	checkpoints  [][]byte
	error        func() error
	ensureBefore time.Duration
}

func (b *fakeStateBackend) Checkpoint(data []byte) error {
	b.checkpoints = append(b.checkpoints, data)
	if b.error != nil {
		return b.error()
	}
	return nil
}

func (b *fakeStateBackend) EnsureBefore(d time.Duration) {
	b.ensureBefore = d
}

func (ss *stateSuite) TestImplicitCheckpointAndRead(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)
	st.Lock()

	st.Set("v", 1)
	mSt1 := &mgrState1{A: "foo"}
	st.Set("mgr1", mSt1)
	mSt2 := &mgrState2{C: &Count2{B: 42}}
	st.Set("mgr2", mSt2)

	// implicit checkpoint
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)

	buf := bytes.NewBuffer(b.checkpoints[0])

	st2, err := state.ReadState(nil, buf)
	c.Assert(err, IsNil)
	c.Assert(st2.Modified(), Equals, false)

	st2.Lock()
	defer st2.Unlock()

	var v int
	err = st2.Get("v", &v)
	c.Assert(err, IsNil)
	c.Check(v, Equals, 1)

	var mSt1B mgrState1
	err = st2.Get("mgr1", &mSt1B)
	c.Assert(err, IsNil)
	c.Check(&mSt1B, DeepEquals, mSt1)

	var mSt2B mgrState2
	err = st2.Get("mgr2", &mSt2B)
	c.Assert(err, IsNil)
	c.Check(&mSt2B, DeepEquals, mSt2)
}

func (ss *stateSuite) TestImplicitCheckpointRetry(c *C) {
	restore := state.MockCheckpointRetryDelay(2*time.Millisecond, 1*time.Second)
	defer restore()

	retries := 0
	boom := errors.New("boom")
	error := func() error {
		retries++
		if retries == 2 {
			return nil
		}
		return boom
	}
	b := &fakeStateBackend{error: error}
	st := state.New(b)
	st.Lock()

	// implicit checkpoint will retry
	st.Unlock()

	c.Check(retries, Equals, 2)
}

func (ss *stateSuite) TestImplicitCheckpointPanicsAfterFailedRetries(c *C) {
	restore := state.MockCheckpointRetryDelay(2*time.Millisecond, 10*time.Millisecond)
	defer restore()

	boom := errors.New("boom")
	retries := 0
	error := func() error {
		retries++
		return boom
	}
	b := &fakeStateBackend{error: error}
	st := state.New(b)
	st.Lock()

	// implicit checkpoint will panic after all failed retries
	t0 := time.Now()
	c.Check(func() { st.Unlock() }, PanicMatches, "cannot checkpoint even after 10ms of retries every 2ms: boom")
	// we did at least a couple
	c.Check(retries > 2, Equals, true)
	c.Check(time.Since(t0) > 10*time.Millisecond, Equals, true)
}

func (ss *stateSuite) TestImplicitCheckpointModifiedOnly(c *C) {
	restore := state.MockCheckpointRetryDelay(2*time.Millisecond, 1*time.Second)
	defer restore()

	b := &fakeStateBackend{}
	st := state.New(b)
	st.Lock()
	st.Unlock()
	st.Lock()
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)

	st.Lock()
	st.Set("foo", "bar")
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 2)
}

func (ss *stateSuite) TestNewChangeAndChanges(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	chg1 := st.NewChange("install", "...")
	chg2 := st.NewChange("remove", "...")

	chgs := st.Changes()
	c.Check(chgs, HasLen, 2)

	expected := map[string]*state.Change{
		chg1.ID(): chg1,
		chg2.ID(): chg2,
	}

	for _, chg := range chgs {
		c.Check(chg, Equals, expected[chg.ID()])
		c.Check(st.Change(chg.ID()), Equals, chg)
	}

	c.Check(st.Change("no-such-id"), IsNil)
}

func (ss *stateSuite) TestNewChangeAndCheckpoint(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)
	st.Lock()

	chg := st.NewChange("install", "summary")
	c.Assert(chg, NotNil)
	chgID := chg.ID()
	chg.Set("a", 1)
	chg.SetStatus(state.ErrorStatus)

	spawnTime := chg.SpawnTime()
	readyTime := chg.ReadyTime()

	// implicit checkpoint
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)

	buf := bytes.NewBuffer(b.checkpoints[0])

	st2, err := state.ReadState(nil, buf)
	c.Assert(err, IsNil)
	c.Assert(st2, NotNil)

	st2.Lock()
	defer st2.Unlock()

	chgs := st2.Changes()

	c.Assert(chgs, HasLen, 1)

	chg0 := chgs[0]
	c.Check(chg0.ID(), Equals, chgID)
	c.Check(chg0.Kind(), Equals, "install")
	c.Check(chg0.Summary(), Equals, "summary")
	c.Check(chg0.SpawnTime().Equal(spawnTime), Equals, true)
	c.Check(chg0.ReadyTime().Equal(readyTime), Equals, true)

	var v int
	err = chg0.Get("a", &v)
	c.Check(v, Equals, 1)

	c.Check(chg0.Status(), Equals, state.ErrorStatus)

	select {
	case <-chg0.Ready():
	default:
		c.Errorf("Change didn't preserve Ready channel closed after deserialization")
	}
}

func (ss *stateSuite) TestNewChangeAndCheckpointTaskDerivedStatus(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)
	st.Lock()

	chg := st.NewChange("install", "summary")
	c.Assert(chg, NotNil)
	chgID := chg.ID()

	t1 := st.NewTask("download", "1...")
	t1.SetStatus(state.DoneStatus)
	chg.AddTask(t1)

	// implicit checkpoint
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)
	buf := bytes.NewBuffer(b.checkpoints[0])

	st2, err := state.ReadState(nil, buf)
	c.Assert(err, IsNil)

	st2.Lock()
	defer st2.Unlock()

	chgs := st2.Changes()

	c.Assert(chgs, HasLen, 1)

	chg0 := chgs[0]
	c.Check(chg0.ID(), Equals, chgID)
	c.Check(chg0.Status(), Equals, state.DoneStatus)

	select {
	case <-chg0.Ready():
	default:
		c.Errorf("Change didn't preserve Ready channel closed after deserialization")
	}
}

func (ss *stateSuite) TestNewTaskAndCheckpoint(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)
	st.Lock()

	chg := st.NewChange("install", "summary")
	c.Assert(chg, NotNil)

	t1 := st.NewTask("download", "1...")
	chg.AddTask(t1)
	t1ID := t1.ID()
	t1.Set("a", 1)
	t1.SetStatus(state.DoneStatus)
	t1.SetProgress(5, 10)

	t2 := st.NewTask("inst", "2...")
	chg.AddTask(t2)
	t2ID := t2.ID()
	t2.WaitFor(t1)

	// implicit checkpoint
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)

	buf := bytes.NewBuffer(b.checkpoints[0])

	st2, err := state.ReadState(nil, buf)
	c.Assert(err, IsNil)
	c.Assert(st2, NotNil)

	st2.Lock()
	defer st2.Unlock()

	chgs := st2.Changes()
	c.Assert(chgs, HasLen, 1)
	chg0 := chgs[0]

	tasks0 := make(map[string]*state.Task)
	for _, t := range chg0.Tasks() {
		tasks0[t.ID()] = t
	}
	c.Assert(tasks0, HasLen, 2)

	task0_1 := tasks0[t1ID]
	c.Check(task0_1.ID(), Equals, t1ID)
	c.Check(task0_1.Kind(), Equals, "download")
	c.Check(task0_1.Summary(), Equals, "1...")
	c.Check(task0_1.Change(), Equals, chg0)

	var v int
	err = task0_1.Get("a", &v)
	c.Check(v, Equals, 1)

	c.Check(task0_1.Status(), Equals, state.DoneStatus)

	cur, tot := task0_1.Progress()
	c.Check(cur, Equals, 5)
	c.Check(tot, Equals, 10)

	task0_2 := tasks0[t2ID]
	c.Check(task0_2.WaitTasks(), DeepEquals, []*state.Task{task0_1})

	c.Check(task0_1.HaltTasks(), DeepEquals, []*state.Task{task0_2})

	tasks2 := make(map[string]*state.Task)
	for _, t := range st2.Tasks() {
		tasks2[t.ID()] = t
	}
	c.Assert(tasks2, HasLen, 2)
}

func (ss *stateSuite) TestEnsureBefore(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)

	st.EnsureBefore(10 * time.Second)

	c.Check(b.ensureBefore, Equals, 10*time.Second)
}

func (ss *stateSuite) TestCheckpointPreserveLastIds(c *C) {
	b := new(fakeStateBackend)
	st := state.New(b)
	st.Lock()

	st.NewChange("install", "...")
	st.NewTask("download", "...")
	st.NewTask("download", "...")

	// implicit checkpoint
	st.Unlock()

	c.Assert(b.checkpoints, HasLen, 1)

	buf := bytes.NewBuffer(b.checkpoints[0])

	st2, err := state.ReadState(nil, buf)
	c.Assert(err, IsNil)

	st2.Lock()
	defer st2.Unlock()

	c.Assert(st2.NewTask("download", "...").ID(), Equals, "3")
	c.Assert(st2.NewChange("install", "...").ID(), Equals, "2")
}

func (ss *stateSuite) TestNewTaskAndTasks(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	chg1 := st.NewChange("install", "...")
	t11 := st.NewTask("check", "...")
	chg1.AddTask(t11)
	t12 := st.NewTask("inst", "...")
	chg1.AddTask(t12)

	chg2 := st.NewChange("remove", "...")
	t21 := st.NewTask("check", "...")
	t22 := st.NewTask("rm", "...")
	chg2.AddTask(t21)
	chg2.AddTask(t22)

	tasks := st.Tasks()
	c.Check(tasks, HasLen, 4)

	expected := map[string]*state.Task{
		t11.ID(): t11,
		t12.ID(): t12,
		t21.ID(): t21,
		t22.ID(): t22,
	}

	for _, t := range tasks {
		c.Check(t, Equals, expected[t.ID()])
	}
}

func (ss *stateSuite) TestTaskNoTask(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	c.Check(st.Task("1"), IsNil)
}

func (ss *stateSuite) TestNewTaskHiddenUntilLinked(c *C) {
	st := state.New(nil)
	st.Lock()
	defer st.Unlock()

	t1 := st.NewTask("check", "...")

	tasks := st.Tasks()
	c.Check(tasks, HasLen, 0)

	c.Check(st.Task(t1.ID()), IsNil)
}

func (ss *stateSuite) TestMethodEntrance(c *C) {
	st := state.New(&fakeStateBackend{})

	// Reset modified flag.
	st.Lock()
	st.Unlock()

	writes := []func(){
		func() { st.Set("foo", 1) },
		func() { st.NewChange("install", "...") },
		func() { st.NewTask("download", "...") },
		func() { st.UnmarshalJSON(nil) },
	}

	reads := []func(){
		func() { st.Get("foo", nil) },
		func() { st.Cached("foo") },
		func() { st.Cache("foo", 1) },
		func() { st.Changes() },
		func() { st.Change("foo") },
		func() { st.Tasks() },
		func() { st.Task("foo") },
		func() { st.MarshalJSON() },
		func() { st.Prune(time.Hour, time.Hour) },
		func() { st.NumTask() },
	}

	for i, f := range reads {
		c.Logf("Testing read function #%d", i)
		c.Assert(f, PanicMatches, "internal error: accessing state without lock")
		c.Assert(st.Modified(), Equals, false)
	}

	for i, f := range writes {
		st.Lock()
		st.Unlock()
		c.Assert(st.Modified(), Equals, false)

		c.Logf("Testing write function #%d", i)
		c.Assert(f, PanicMatches, "internal error: accessing state without lock")
		c.Assert(st.Modified(), Equals, true)
	}
}

func (ss *stateSuite) TestPrune(c *C) {
	st := state.New(&fakeStateBackend{})
	st.Lock()
	defer st.Unlock()

	now := time.Now()
	pruneWait := 1 * time.Hour
	abortWait := 3 * time.Hour

	unset := time.Time{}

	t1 := st.NewTask("foo", "...")
	t2 := st.NewTask("foo", "...")
	t3 := st.NewTask("foo", "...")
	t4 := st.NewTask("foo", "...")

	chg1 := st.NewChange("abort", "...")
	chg1.AddTask(t1)
	state.MockChangeTimes(chg1, now.Add(-abortWait), unset)

	chg2 := st.NewChange("prune", "...")
	chg2.AddTask(t2)
	c.Assert(chg2.Status(), Equals, state.DoStatus)
	state.MockChangeTimes(chg2, now.Add(-pruneWait), now.Add(-pruneWait))

	chg3 := st.NewChange("ready-but-recent", "...")
	chg3.AddTask(t3)
	state.MockChangeTimes(chg3, now.Add(-pruneWait), now.Add(-pruneWait/2))

	chg4 := st.NewChange("old-but-not-ready", "...")
	chg4.AddTask(t4)
	state.MockChangeTimes(chg4, now.Add(-pruneWait/2), unset)

	// unlinked task
	t5 := st.NewTask("unliked", "...")
	c.Check(st.Task(t5.ID()), IsNil)
	state.MockTaskTimes(t5, now.Add(-pruneWait), now.Add(-pruneWait))

	st.Prune(pruneWait, abortWait)

	c.Assert(st.Change(chg1.ID()), Equals, chg1)
	c.Assert(st.Change(chg2.ID()), IsNil)
	c.Assert(st.Change(chg3.ID()), Equals, chg3)
	c.Assert(st.Change(chg4.ID()), Equals, chg4)

	c.Assert(st.Task(t1.ID()), Equals, t1)
	c.Assert(st.Task(t2.ID()), IsNil)
	c.Assert(st.Task(t3.ID()), Equals, t3)
	c.Assert(st.Task(t4.ID()), Equals, t4)

	c.Assert(chg1.Status(), Equals, state.HoldStatus)
	c.Assert(chg3.Status(), Equals, state.DoStatus)
	c.Assert(chg4.Status(), Equals, state.DoStatus)

	c.Assert(t1.Status(), Equals, state.HoldStatus)
	c.Assert(t3.Status(), Equals, state.DoStatus)
	c.Assert(t4.Status(), Equals, state.DoStatus)

	c.Check(st.NumTask(), Equals, 3)
}
