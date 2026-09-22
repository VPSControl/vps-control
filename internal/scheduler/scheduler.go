package scheduler

import (
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"vpscontrol/internal/store"
)

type Runner func(schedule store.Schedule) (string, error)

type Scheduler struct {
	mu       sync.Mutex
	cron     *cron.Cron
	store    *store.Store
	runner   Runner
	entryIDs map[string]cron.EntryID
}

func New(st *store.Store, runner Runner) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		store:    st,
		runner:   runner,
		entryIDs: map[string]cron.EntryID{},
	}
}

func (s *Scheduler) Start() {
	s.Reload()
	s.cron.Start()
	log.Printf("[scheduler] started")
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

func (s *Scheduler) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.entryIDs {
		s.cron.Remove(id)
	}
	s.entryIDs = map[string]cron.EntryID{}

	schedules := s.store.ListAllSchedules()
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		expr := "0 " + sc.CronExpr
		schedule := sc
		id, err := s.cron.AddFunc(expr, func() {
			s.run(schedule)
		})
		if err != nil {
			log.Printf("[scheduler] invalid cron for %s (%s): %v", sc.Name, sc.CronExpr, err)
			continue
		}
		s.entryIDs[sc.ID] = id
	}
	log.Printf("[scheduler] loaded %d schedules", len(s.entryIDs))
}

func (s *Scheduler) RunNow(scheduleID string) error {
	sc, ok := s.store.FindScheduleByID(scheduleID)
	if !ok {
		return nil
	}
	s.run(sc)
	return nil
}

func (s *Scheduler) run(sc store.Schedule) {
	log.Printf("[scheduler] running %q (%s)", sc.Name, sc.Action)

	output, err := s.runner(sc)

	sc.LastRunAt = time.Now()
	if err != nil {
		sc.LastStatus = "error"
		sc.LastOutput = err.Error()
		log.Printf("[scheduler] %q failed: %v", sc.Name, err)
	} else {
		sc.LastStatus = "ok"
		sc.LastOutput = output
		if len(sc.LastOutput) > 500 {
			sc.LastOutput = sc.LastOutput[:500] + "..."
		}
	}
	_ = s.store.UpdateSchedule(sc)
}