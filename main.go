package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jaypipes/ghw"
)

const parentCgroup = "limaqcgroup"

var flagVerbose bool

func getCgroupPath(subname string) string {
	return fmt.Sprintf("%s/%s", parentCgroup, subname)
}

func prefixCgroupCnt(path string) string {
	return fmt.Sprintf("blkio,memory,cpu:%s", path)
}

func execCmd(name string, args ...string) error {
	if flagVerbose {
		fmtArgs := make([]string, len(args))
		for i, arg := range args {
			fmtArgs[i] = fmt.Sprintf("'%s'", arg)
		}
		fmt.Fprintf(os.Stderr, "exec: '%s' %s\n", name, strings.Join(fmtArgs, " "))
	}

	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dumpCmd(name string, args ...string) ([]byte, error) {
	if flagVerbose {
		fmtArgs := make([]string, len(args))
		for i, arg := range args {
			fmtArgs[i] = fmt.Sprintf("'%s'", arg)
		}
		fmt.Fprintf(os.Stderr, "dump: '%s' %s\n", name, strings.Join(fmtArgs, " "))
	}

	return exec.Command(name, args...).Output()
}

type cgroup struct {
	name  string
	procs []string
}

func listCgroups() (map[string]cgroup, error) {
	src, err := dumpCmd("cgsnapshot")
	if err != nil {
		return nil, err
	}

	cgs := make(map[string]cgroup)

	groups := regexp.
		MustCompile(fmt.Sprintf(`group %s/(........-....-....-....-............) {`, parentCgroup)).
		FindAllStringSubmatch(string(src), -1)
	for _, src := range groups {
		group := src[1]
		name := getCgroupPath(group)

		if _, ok := cgs[name]; ok { // duplicate.
			continue
		}

		file, err := os.Open(fmt.Sprintf("/sys/fs/cgroup/cpu/%s/cgroup.procs", name))
		if err != nil {
			return nil, err
		}
		procsSrc, err := ioutil.ReadAll(file)
		if err != nil {
			return nil, err
		}
		procs := strings.Split(string(procsSrc), "\n")

		cg := cgroup{}
		cg.name = name
		for _, proc := range procs {
			proc = strings.TrimSpace(proc)
			if proc != "" {
				cg.procs = append(cg.procs, proc)
			}
		}

		cgs[cg.name] = cg
	}

	return cgs, nil
}

func isCgroupsAvailable() bool {
	src, err := dumpCmd("cgsnapshot")
	if err != nil {
		return false
	}

	res := regexp.MustCompile(fmt.Sprintf(`group %s {`, parentCgroup)).MatchString(string(src))
	return res
}

func printStatus() error {
	cgs, err := listCgroups()
	if err != nil {
		return err
	}

	for _, cg := range cgs {
		fmt.Printf("%s: ", cg.name)
		for _, proc := range cg.procs {
			fmt.Printf("%s ", proc)
		}
		fmt.Printf("\n")
	}

	return nil
}

func setCgroupAttrs(cgpath string, attrs map[string]interface{}) error {
	var args []string
	for key, value := range attrs {
		args = append(args, "-r", fmt.Sprintf("%s=%v", key, value))
	}
	args = append(args, cgpath)
	return execCmd("cgset", args...)
}

func doRun(ncores float64, memmb float64, ioWeight int) error {
	const cfsPeriod = 100000
	var err error

	uuid, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	cgpath := getCgroupPath(uuid.String())
	cgcntpath := prefixCgroupCnt(cgpath)

	err = execCmd("cgcreate", "-g", cgcntpath)
	if err != nil {
		return err
	}
	defer execCmd("cgdelete", "-r", cgcntpath)

	err = setCgroupAttrs(cgpath, map[string]interface{}{
		"cpu.cfs_period_us":     cfsPeriod,
		"cpu.cfs_quota_us":      int(cfsPeriod * ncores),
		"memory.limit_in_bytes": int(memmb * 1000000),
		"blkio.weight":          ioWeight,
	})
	if err != nil {
		return err
	}

	err = execCmd("cgexec",
		append([]string{"-g", cgcntpath}, flag.Args()...)...)
	if err != nil {
		return err
	}

	return nil
}

func doPrune() error {
	cgs, err := listCgroups()
	if err != nil {
		return err
	}

	for name, cg := range cgs {
		if len(cg.procs) > 0 { // active.
			continue
		}
		err = execCmd("cgdelete", "-r", prefixCgroupCnt(name))
		if err != nil {
			return err
		}

		fmt.Printf("Delete %s\n", name)
	}

	return nil
}

func main() {
	var err error

	if !isCgroupsAvailable() {
		user, err := user.Current()
		if err != nil {
			panic(errors.New("Can't get current user"))
		}
		fmt.Fprintf(os.Stderr, "cgroups is not available. Maybe you should run:\n\n\t# cgcreate -a %s -t %s -g blkio,memory,cpu:%s\n\nto create the parent cgroup.\n", user.Username, user.Username, parentCgroup)
		os.Exit(1)
	}

	cpu, err := ghw.CPU()
	if err != nil {
		panic(err)
	}
	memory, err := ghw.Memory()
	if err != nil {
		panic(err)
	}

	ncores := flag.Float64("cpu", float64(cpu.TotalCores),
		"#cores of CPU you want to use")
	memmb := flag.Float64("mem", float64(memory.TotalPhysicalBytes)/1000000,
		"Memory size in MB you want to use")
	ioWeight := flag.Int("io", 1000,
		"Relative weight of block I/O access from 100 to 1000")
	verbose := flag.Bool("verbose", false, "Verbose mode")
	showStatus := flag.Bool("stat", false, "Show status")
	pruneCgroups := flag.Bool("prune", false, "Remove inactive cgroups")
	flag.Parse()
	flagVerbose = *verbose

	if *showStatus {
		if err = printStatus(); err != nil {
			panic(err)
		}
		os.Exit(0)
	}

	if *pruneCgroups {
		if err = doPrune(); err != nil {
			panic(err)
		}
		os.Exit(0)
	}

	if len(flag.Args()) == 0 {
		fmt.Fprintf(os.Stderr, "Give me a program\n")
		os.Exit(1)
	}

	if *ioWeight < 100 || *ioWeight > 1000 {
		fmt.Fprintf(os.Stderr, "I/O weight should be in the range of from 100 to 1000\n")
		os.Exit(1)
	}

	if flagVerbose {
		fmt.Fprintf(os.Stderr, "CPU:\t%f cores\n", *ncores)
		fmt.Fprintf(os.Stderr, "Memory:\t%f MB\n", *memmb)
		fmt.Fprintf(os.Stderr, "I/O Weight:\t%d\n", *ioWeight)
	}

	if err = doRun(*ncores, *memmb, *ioWeight); err != nil {
		panic(err)
	}
}
