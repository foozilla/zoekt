// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitindex

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/zoekt"
	"github.com/google/zoekt/build"

	git "github.com/libgit2/git2go"
)

// RepoModTime returns the time of last fetch of a git repository.
func RepoModTime(dir string) (time.Time, error) {
	var last time.Time
	refDir := filepath.Join(dir, "refs")
	if _, err := os.Lstat(refDir); err == nil {
		if err := filepath.Walk(refDir,
			func(name string, fi os.FileInfo, err error) error {
				if !fi.IsDir() && last.Before(fi.ModTime()) {
					last = fi.ModTime()
				}
				return nil
			}); err != nil {
			return last, err
		}
	}

	// git gc compresses refs into the following file:
	for _, fn := range []string{"info/refs", "packed-refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, fn)); err == nil && !fi.IsDir() && last.Before(fi.ModTime()) {
			last = fi.ModTime()
		}
	}

	return last, nil
}

// FindGitRepos finds directories holding git repositories.
func FindGitRepos(arg string) ([]string, error) {
	arg, err := filepath.Abs(arg)
	if err != nil {
		return nil, err
	}
	var dirs []string
	if err := filepath.Walk(arg, func(name string, fi os.FileInfo, err error) error {
		if fi, err := os.Lstat(filepath.Join(name, ".git")); err == nil && fi.IsDir() {
			dirs = append(dirs, filepath.Join(name, ".git"))
			return filepath.SkipDir
		}

		if !strings.HasSuffix(name, ".git") || !fi.IsDir() {
			return nil
		}

		fi, err = os.Lstat(filepath.Join(name, "objects"))
		if err != nil || !fi.IsDir() {
			return nil
		}

		dirs = append(dirs, name)
		return filepath.SkipDir
	}); err != nil {
		return nil, err
	}

	return dirs, nil
}

func templatesForOrigin(u *url.URL) (*zoekt.Repository, error) {
	return nil, fmt.Errorf("unknown URL %s", u)
}

// setTemplates fills in URL templates for known git hosting
// sites.
func setTemplates(repo *zoekt.Repository, u *url.URL, typ string) error {
	repo.URL = u.String()
	switch typ {
	case "gitiles":
		/// eg. https://gerrit.googlesource.com/gitiles/+/master/tools/run_dev.sh#20
		repo.CommitURLTemplate = u.String() + "/+/{{.Version}}"
		repo.FileURLTemplate = u.String() + "/+/{{.Version}}/{{.Path}}"
		repo.LineFragmentTemplate = "{{.LineNumber}}"
	case "github":
		// eg. https://github.com/hanwen/go-fuse/blob/notify/genversion.sh#L10
		repo.CommitURLTemplate = u.String() + "/commit/{{.Version}}"
		repo.FileURLTemplate = u.String() + "/blob/{{.Version}}/{{.Path}}"
		repo.LineFragmentTemplate = "L{{.LineNumber}}"

	case "cgit":

		// http://git.savannah.gnu.org/cgit/lilypond.git/tree/elisp/lilypond-mode.el?h=dev/philh&id=b2ca0fefe3018477aaca23b6f672c7199ba5238e#n100

		repo.CommitURLTemplate = u.String() + "/commit/?id={{.Version}}"
		repo.FileURLTemplate = u.String() + "/tree/{{.Path}}/?id={{.Version}}"
		repo.LineFragmentTemplate = "n{{.LineNumber}}"

	case "gitweb":
		// https://gerrit.libreoffice.org/gitweb?p=online.git;a=blob;f=Makefile.am;h=cfcfd7c36fbae10e269653dc57a9b68c92d4c10b;hb=848145503bf7b98ce4a4aa0a858a0d71dd0dbb26#l10
		repo.FileURLTemplate = u.String() + ";a=blob;f={{.Path}};hb={{.Version}}"
		repo.CommitURLTemplate = u.String() + ";a=commit;h={{.Version}}"
		repo.LineFragmentTemplate = "l{{.LineNumber}}"

	default:
		return fmt.Errorf("URL scheme type %q unknown", typ)
	}
	return nil
}

// getCommit returns a tree object for the given reference.
func getCommit(repo *git.Repository, ref string) (*git.Commit, error) {
	obj, err := repo.RevparseSingle(ref)
	if err != nil {
		return nil, err
	}
	defer obj.Free()

	commitObj, err := obj.Peel(git.ObjectCommit)
	if err != nil {
		return nil, err
	}
	return commitObj.AsCommit()
}

func clearEmptyConfig(err error) error {
	if git.IsErrorClass(err, git.ErrClassConfig) && git.IsErrorCode(err, git.ErrNotFound) {
		return nil
	}
	return err
}

func isMissingBranchError(err error) bool {
	return git.IsErrorClass(err, git.ErrClassReference) && git.IsErrorCode(err, git.ErrNotFound)
}

func setTemplatesFromConfig(desc *zoekt.Repository, repoDir string) error {
	base, err := git.NewConfig()
	if err != nil {
		return err
	}
	defer base.Free()
	cfg, err := git.OpenOndisk(base, filepath.Join(repoDir, "config"))
	if err != nil {
		return err
	}
	defer cfg.Free()

	webURLStr, err := cfg.LookupString("zoekt.web-url")
	err = clearEmptyConfig(err)
	if err != nil {
		return err
	}

	webURLType, err := cfg.LookupString("zoekt.web-url-type")
	err = clearEmptyConfig(err)
	if err != nil {
		return err
	}

	if webURLType != "" && webURLStr != "" {
		webURL, err := url.Parse(webURLStr)
		if err != nil {
			return err
		}
		if err := setTemplates(desc, webURL, webURLType); err != nil {
			return err
		}
	}

	name, err := cfg.LookupString("zoekt.name")
	err = clearEmptyConfig(err)
	if err != nil {
		return err
	}

	if name != "" {
		desc.Name = name
	} else {
		remoteURL, err := cfg.LookupString("remote.origin.url")
		err = clearEmptyConfig(err)
		if err != nil || remoteURL == "" {
			return err
		}
		u, err := url.Parse(remoteURL)
		if err != nil {
			return err
		}
		if err := SetTemplatesFromOrigin(desc, u); err != nil {
			return err
		}
	}
	return nil
}

// SetTemplates fills in templates based on the origin URL.
func SetTemplatesFromOrigin(desc *zoekt.Repository, u *url.URL) error {
	desc.Name = filepath.Join(u.Host, strings.TrimSuffix(u.Path, ".git"))

	if strings.HasSuffix(u.Host, ".googlesource.com") {
		return setTemplates(desc, u, "gitiles")
	} else if u.Host == "github.com" {
		u.Path = strings.TrimSuffix(u.Path, ".git")
		return setTemplates(desc, u, "github")
	} else {
		return fmt.Errorf("unknown git hosting site %q", u)
	}

	found, err := templatesForOrigin(u)

	if err != nil {
		return err

	}

	desc.URL = found.URL
	desc.CommitURLTemplate = found.CommitURLTemplate
	desc.FileURLTemplate = found.FileURLTemplate
	desc.LineFragmentTemplate = found.LineFragmentTemplate
	return nil
}

type Options struct {
	Submodules         bool
	Incremental        bool
	AllowMissingBranch bool
	RepoCacheDir       string
	BuildOptions       build.Options

	BranchPrefix string
	Branches     []string
}

func expandBranches(repo *git.Repository, bs []string, prefix string) ([]string, error) {
	var result []string
	for _, b := range bs {
		if b == "HEAD" {
			_, ref, err := repo.RevparseExt(b)
			if err != nil {
				return nil, err
			}

			result = append(result, strings.TrimPrefix(ref.Name(), prefix))
			continue
		}

		if strings.Contains(b, "*") {
			iter, err := repo.NewBranchIterator(git.BranchAll)
			if err != nil {
				log.Println("boem")
				return nil, err
			}

			for {
				br, _, err := iter.Next()
				if git.IsErrorCode(err, git.ErrIterOver) {
					break
				}
				if err != nil {
					log.Printf("bam %#v", err)
					return nil, err
				}

				name, err := br.Name()
				if err != nil {
					return nil, err
				}
				if matched, err := filepath.Match(b, name); err != nil {
					return nil, err
				} else if !matched {
					continue
				}

				result = append(result, strings.TrimPrefix(name, prefix))
			}
			continue
		}

		result = append(result, b)
	}

	return result, nil

}

// IndexGitRepo indexes the git repository as specified by the options.
func IndexGitRepo(opts Options) error {
	repo, err := git.OpenRepository(opts.BuildOptions.RepoDir)
	if err != nil {
		return err
	}

	if err := setTemplatesFromConfig(&opts.BuildOptions.RepositoryDescription, opts.BuildOptions.RepoDir); err != nil {
		log.Printf("setTemplatesFromConfig(%s): %s", opts.BuildOptions.RepoDir, err)
	}

	repoCache := NewRepoCache(opts.RepoCacheDir)
	defer repoCache.Close()

	// branch => (path, sha1) => repo.
	repos := map[FileKey]BlobLocation{}

	// FileKey => branches
	branchMap := map[FileKey][]string{}

	// Branch => Repo => SHA1
	branchVersions := map[string]map[string]git.Oid{}

	branches, err := expandBranches(repo, opts.Branches, opts.BranchPrefix)
	if err != nil {
		return err
	}
	for _, b := range branches {
		fullName := filepath.Join(opts.BranchPrefix, b)

		commit, err := getCommit(repo, fullName)
		if opts.AllowMissingBranch && isMissingBranchError(err) {
			continue
		}

		if err != nil {
			return err
		}
		defer commit.Free()
		opts.BuildOptions.RepositoryDescription.Branches = append(opts.BuildOptions.RepositoryDescription.Branches, zoekt.RepositoryBranch{
			Name:    b,
			Version: commit.Id().String(),
		})

		tree, err := commit.Tree()
		if err != nil {
			return err
		}
		defer tree.Free()

		files, subVersions, err := TreeToFiles(repo, tree, opts.BuildOptions.RepositoryDescription.URL, repoCache)
		if err != nil {
			return err
		}
		for k, v := range files {
			repos[k] = v
			branchMap[k] = append(branchMap[k], b)
		}

		branchVersions[b] = subVersions
	}

	if opts.Incremental {
		versions := opts.BuildOptions.IndexVersions()
		if reflect.DeepEqual(versions, opts.BuildOptions.RepositoryDescription.Branches) {
			return nil
		}
	}

	reposByPath := map[string]BlobLocation{}
	for key, location := range repos {
		reposByPath[key.SubRepoPath] = location
	}

	opts.BuildOptions.SubRepositories = map[string]*zoekt.Repository{}
	for path, location := range reposByPath {
		tpl := opts.BuildOptions.RepositoryDescription
		if path != "" {
			tpl = zoekt.Repository{URL: location.URL.String()}
			if err := SetTemplatesFromOrigin(&tpl, location.URL); err != nil {
				log.Printf("setTemplatesFromOrigin(%s, %s): %s", path, location.URL, err)
			}
		}
		opts.BuildOptions.SubRepositories[path] = &tpl
	}
	for _, br := range opts.BuildOptions.RepositoryDescription.Branches {
		for path, repo := range opts.BuildOptions.SubRepositories {
			id := branchVersions[br.Name][path]
			repo.Branches = append(repo.Branches, zoekt.RepositoryBranch{
				Name:    br.Name,
				Version: id.String(),
			})
		}
	}

	builder, err := build.NewBuilder(opts.BuildOptions)
	if err != nil {
		return err
	}

	var names []string
	fileKeys := map[string][]FileKey{}
	for key := range repos {
		n := key.FullPath()
		fileKeys[n] = append(fileKeys[n], key)
		names = append(names, n)
	}
	// not strictly necessary, but nice for reproducibility.
	sort.Strings(names)

	for _, name := range names {
		keys := fileKeys[name]
		for _, key := range keys {
			brs := branchMap[key]
			blob, err := repos[key].Repo.LookupBlob(&key.ID)
			if err != nil {
				return err
			}

			if blob.Size() > int64(opts.BuildOptions.SizeMax) {
				continue
			}

			builder.Add(zoekt.Document{
				SubRepositoryPath: key.SubRepoPath,
				Name:              key.FullPath(),
				Content:           blob.Contents(),
				Branches:          brs,
			})
		}
	}
	return builder.Finish()
}
