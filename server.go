package main

import (
	"code.google.com/p/go-uuid/uuid"
	"github.com/clawio/service.auth/lib"
	pb "github.com/clawio/service.localstore.prop/proto/propagator"
	"github.com/jinzhu/gorm"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"path"
	"strings"
	"time"
)

const (
	dirPerm = 0755
)

var (
	unauthenticatedError = grpc.Errorf(codes.Unauthenticated, "identity not found")
	permissionDenied     = grpc.Errorf(codes.PermissionDenied, "access denied")
)

type newServerParams struct {
	dsn          string
	db           *gorm.DB
	sharedSecret string
}

func newServer(p *newServerParams) (*server, error) {

	db, err := newDB("mysql", p.dsn)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	db.LogMode(true)

	err = db.AutoMigrate(&record{}).Error
	if err != nil {
		log.Error(err)
		return nil, err
	}

	log.Infof("automigration applied")

	s := &server{}
	s.p = p
	s.db = db
	return s, nil
}

type server struct {
	p  *newServerParams
	db *gorm.DB
}

func (s *server) Get(ctx context.Context, req *pb.GetReq) (*pb.Record, error) {

	traceID := getGRPCTraceID(ctx)
	log := log.WithField("trace", traceID)
	ctx = newGRPCTraceContext(ctx, traceID)

	idt, err := lib.ParseToken(req.AccessToken, s.p.sharedSecret)
	if err != nil {
		log.Error(err)
		return &pb.Record{}, unauthenticatedError
	}

	log.Infof("%s", idt)

	p := path.Clean(req.Path)

	log.Infof("path is %s", p)

	var rec *record

	rec, err = s.getByPath(p)
	if err != nil {
		log.Error(err)
		if err != gorm.RecordNotFound {
			return &pb.Record{}, err
		}

		if !req.ForceCreation {
			return &pb.Record{}, err
		}

		if req.ForceCreation {
			in := &pb.PutReq{}
			in.AccessToken = req.AccessToken
			in.Path = req.Path
			_, e := s.Put(ctx, in)
			if e != nil {
				return &pb.Record{}, err
			}

			rec, err = s.getByPath(p)
			if err != nil {
				return &pb.Record{}, nil
			}
		}
	}

	r := &pb.Record{}
	r.Id = rec.ID
	r.Path = rec.Path
	r.Etag = rec.ETag
	r.Modified = rec.MTime
	r.Checksum = rec.Checksum
	return r, nil
}

func (s *server) Mv(ctx context.Context, req *pb.MvReq) (*pb.Void, error) {

	traceID := getGRPCTraceID(ctx)
	log := log.WithField("trace", traceID)
	ctx = newGRPCTraceContext(ctx, traceID)

	idt, err := lib.ParseToken(req.AccessToken, s.p.sharedSecret)
	if err != nil {
		log.Error(err)
		return &pb.Void{}, unauthenticatedError
	}

	log.Infof("%s", idt)

	src := path.Clean(req.Src)
	dst := path.Clean(req.Dst)

	log.Infof("src path is %s", src)
	log.Infof("dst path is %s", dst)

	//TODO implement rename in db
	recs, err := s.getRecordsWithPathPrefix(src)
	if err != nil {
		return &pb.Void{}, nil
	}

	etag := uuid.New()
	mtime := uint32(time.Now().Unix())

	tx := s.db.Begin()
	for _, rec := range recs {
		newPath := path.Join(dst, path.Clean(strings.Trim(rec.Path, src)))
		log.Infof("src path %s will be renamed to %s", src, newPath)

		err = s.db.Model(record{}).Where("id=?", rec.ID).Updates(record{ETag: etag, MTime: mtime, Path: newPath}).Error
		if err != nil {
			log.Error(err)
			tx.Rollback()
			return &pb.Void{}, err
		}
	}
	tx.Commit()

	log.Infof("renamed %d entries", len(recs))

	err = s.propagateChanges(dst, etag, mtime, "")
	if err != nil {
		log.Error(err)
	}

	log.Infof("propagated changes till %s", "")

	return &pb.Void{}, nil
}

func (s *server) getRecordsWithPathPrefix(p string) ([]record, error) {

	var recs []record

	err := s.db.Where("path LIKE ?", p+"%").Find(&recs).Error
	if err != nil {
		log.Error(err)
		return recs, nil
	}

	return recs, nil
}
func (s *server) Rm(ctx context.Context, req *pb.RmReq) (*pb.Void, error) {

	traceID := getGRPCTraceID(ctx)
	log := log.WithField("trace", traceID)
	ctx = newGRPCTraceContext(ctx, traceID)

	idt, err := lib.ParseToken(req.AccessToken, s.p.sharedSecret)
	if err != nil {
		log.Error(err)
		return &pb.Void{}, unauthenticatedError
	}

	log.Infof("%s", idt)

	p := path.Clean(req.Path)

	log.Infof("path is %s", p)

	ts := time.Now().Unix()
	err = s.db.Where("path LIKE ? AND m_time < ?", p+"%", ts).Delete(record{}).Error
	if err != nil {
		log.Error(err)
		return &pb.Void{}, err
	}

	err = s.propagateChanges(p, uuid.New(), uint32(ts), "")
	if err != nil {
		log.Error(err)
	}

	log.Infof("propagated changes till %s", "")

	return &pb.Void{}, nil
}

func (s *server) Put(ctx context.Context, req *pb.PutReq) (*pb.Void, error) {

	traceID := getGRPCTraceID(ctx)
	log := log.WithField("trace", traceID)
	ctx = newGRPCTraceContext(ctx, traceID)

	idt, err := lib.ParseToken(req.AccessToken, s.p.sharedSecret)
	if err != nil {
		log.Error(err)
		return &pb.Void{}, unauthenticatedError
	}

	log.Infof("%s", idt)

	p := path.Clean(req.Path)

	log.Infof("path is %s", p)

	var id string
	var etag = uuid.New()
	var mtime = uint32(time.Now().Unix())

	r, err := s.getByPath(p)
	if err != nil {
		log.Error(err)
		if err == gorm.RecordNotFound {
			id = uuid.New()
		} else {
			return &pb.Void{}, err
		}
	} else {
		id = r.ID
	}

	log.Infof("new record will have id=%s path=%s checksum=%s etag=%s mtime=%d", id, p, req.Checksum, etag, mtime)

	err = s.insert(id, p, req.Checksum, etag, mtime)
	if err != nil {
		log.Error(err)
		return &pb.Void{}, err
	}

	log.Infof("new record saved to db")

	err = s.propagateChanges(p, etag, mtime, "")
	if err != nil {
		log.Error(err)
	}

	log.Infof("propagated changes till ancestor %s", "")

	return &pb.Void{}, nil
}

func (s *server) getByPath(path string) (*record, error) {

	r := &record{}
	err := s.db.Where("path=?", path).First(r).Error
	return r, err
}

func (s *server) insert(id, p, checksum, etag string, mtime uint32) error {

	err := s.db.Exec(`INSERT INTO records (id,path,checksum, e_tag, m_time) VALUES (?,?,?,?,?)
	ON DUPLICATE KEY UPDATE checksum=VALUES(checksum), e_tag=VALUES(e_tag), m_time=VALUES(m_time)`,
		id, p, checksum, etag, mtime).Error

	if err != nil {
		return err
	}

	return nil
}
func (s *server) update(p, etag string, mtime uint32) int64 {

	return s.db.Model(record{}).Where("path=? AND m_time < ?", p, mtime).Updates(record{ETag: etag, MTime: mtime}).RowsAffected
}

// propagateChanges propagates mtime and etag until the user home directory
// This propagation is needed for the client discovering changes
// Ex: given the succesfull upload of the file /local/users/d/demo/photos/1.png
// the etag and mtime will be propagated to:
//    - /local/users/d/demo/photos
//    - /local/users/d/demo
func (s *server) propagateChanges(p, etag string, mtime uint32, stopPath string) error {

	paths := getPathsTillHome(p)
	for _, p := range paths {
		numRows := s.update(p, etag, mtime)
		if numRows == 0 {
			log.Warnf("parent path %s has not being updated with etag=%s and mtime=%s", p, etag, mtime)
		} else {
			log.Infof("parent path %s has being updated", p)
		}
	}

	return nil
}

// TODO remove current dir from returned list
func getPathsTillHome(p string) []string {

	paths := []string{}
	tokens := strings.Split(p, "/")

	homeTokens := tokens[0:5]
	restTokens := tokens[5:]

	home := path.Clean("/" + path.Join(homeTokens...))

	previous := home
	paths = append(paths, previous)

	for _, token := range restTokens {
		previous = path.Join(previous, path.Clean(token))
		paths = append(paths, previous)
	}

	log.Infof("paths for update %+v", paths)

	return paths
}
