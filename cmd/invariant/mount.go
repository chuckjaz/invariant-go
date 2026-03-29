package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"

	"github.com/hanwen/go-fuse/v2/fs"

	"invariant/internal/config"
	"invariant/internal/fuse"
)

func runMount(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("mount", flag.ExitOnError)
	var mountpoint string
	fsFlags.StringVar(&mountpoint, "mount", "", "Directory to mount the FUSE file system")

	var commonFlags CommonMountFlags
	commonFlags.Register(fsFlags)

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant mount [options]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	if mountpoint == "" {
		log.Fatalf("Mountpoint is required (--mount)")
	}

	filesrv := SetupFileSystem(globalCfg, &commonFlags)
	defer filesrv.Close()

	rootNode := fuse.NewNode(filesrv, 1) // 1 is the root node ID in InMemoryFiles

	var uid, gid uint32
	if currentUser, err := user.Current(); err == nil {
		if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
			uid = uint32(parsedUID)
		}
		if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
			gid = uint32(parsedGID)
		}
	}

	server, err := fs.Mount(mountpoint, rootNode, &fs.Options{
		UID: uid,
		GID: gid,
	})
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	log.Printf("Mounted on %s\n", mountpoint)
	log.Printf("Unmount by calling 'fusermount -u %s'", mountpoint)
	server.Wait()
}
