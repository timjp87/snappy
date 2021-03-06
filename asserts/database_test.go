// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015-2016 Canonical Ltd
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

package asserts_test

import (
	"bytes"
	"crypto"
	"encoding/base64"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"golang.org/x/crypto/openpgp/packet"
	. "gopkg.in/check.v1"

	"github.com/ubuntu-core/snappy/asserts"
	"github.com/ubuntu-core/snappy/osutil"
)

func Test(t *testing.T) { TestingT(t) }

var _ = Suite(&openSuite{})
var _ = Suite(&revisionErrorSuite{})

type openSuite struct{}

func (opens *openSuite) TestOpenDatabaseOK(c *C) {
	cfg := &asserts.DatabaseConfig{
		KeypairManager: asserts.NewMemoryKeypairManager(),
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)
	c.Assert(db, NotNil)
}

func (opens *openSuite) TestOpenDatabasePanicOnUnsetBackstores(c *C) {
	cfg := &asserts.DatabaseConfig{}
	c.Assert(func() { asserts.OpenDatabase(cfg) }, PanicMatches, "database cannot be used without setting a keypair manager")
}

type databaseSuite struct {
	topDir string
	db     *asserts.Database
}

var _ = Suite(&databaseSuite{})

func (dbs *databaseSuite) SetUpTest(c *C) {
	dbs.topDir = filepath.Join(c.MkDir(), "asserts-db")
	fsKeypairMgr, err := asserts.OpenFSKeypairManager(dbs.topDir)
	c.Assert(err, IsNil)
	cfg := &asserts.DatabaseConfig{
		KeypairManager: fsKeypairMgr,
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)
	dbs.db = db
}

func (dbs *databaseSuite) TestImportKey(c *C) {
	expectedFingerprint := hex.EncodeToString(testPrivKey1.PublicKey.Fingerprint[:])
	expectedKeyID := hex.EncodeToString(testPrivKey1.PublicKey.Fingerprint[12:])

	err := dbs.db.ImportKey("account0", asserts.OpenPGPPrivateKey(testPrivKey1))
	c.Assert(err, IsNil)

	keyPath := filepath.Join(dbs.topDir, "private-keys-v0/account0", expectedKeyID)
	info, err := os.Stat(keyPath)
	c.Assert(err, IsNil)
	c.Check(info.Mode().Perm(), Equals, os.FileMode(0600)) // secret
	// too white box? ok at least until we have more functionality
	privKey, err := ioutil.ReadFile(keyPath)
	c.Assert(err, IsNil)

	privKeyFromDisk, err := asserts.DecodePrivateKeyInTest(privKey)
	c.Assert(err, IsNil)

	c.Check(privKeyFromDisk.PublicKey().Fingerprint(), Equals, expectedFingerprint)
}

func (dbs *databaseSuite) TestImportKeyAlreadyExists(c *C) {
	err := dbs.db.ImportKey("account0", asserts.OpenPGPPrivateKey(testPrivKey1))
	c.Assert(err, IsNil)

	err = dbs.db.ImportKey("account0", asserts.OpenPGPPrivateKey(testPrivKey1))
	c.Check(err, ErrorMatches, "key pair with given key id already exists")
}

func (dbs *databaseSuite) TestGenerateKey(c *C) {
	fingerp, err := dbs.db.GenerateKey("account0")
	c.Assert(err, IsNil)
	c.Check(fingerp, NotNil)
	keyPath := filepath.Join(dbs.topDir, "private-keys-v0/account0", fingerp)
	c.Check(osutil.FileExists(keyPath), Equals, true)
}

func (dbs *databaseSuite) TestPublicKey(c *C) {
	pk := asserts.OpenPGPPrivateKey(testPrivKey1)
	fingerp := pk.PublicKey().Fingerprint()
	keyid := pk.PublicKey().ID()
	err := dbs.db.ImportKey("account0", pk)
	c.Assert(err, IsNil)

	pubk, err := dbs.db.PublicKey("account0", keyid)
	c.Assert(err, IsNil)
	c.Check(pubk.Fingerprint(), Equals, fingerp)

	// usual pattern is to then encode it
	encoded, err := asserts.EncodePublicKey(pubk)
	c.Assert(err, IsNil)
	c.Check(bytes.HasPrefix(encoded, []byte("openpgp ")), Equals, true)
	data, err := base64.StdEncoding.DecodeString(string(encoded[len("openpgp "):]))
	c.Assert(err, IsNil)
	pkt, err := packet.Read(bytes.NewBuffer(data))
	c.Assert(err, IsNil)
	pubKey, ok := pkt.(*packet.PublicKey)
	c.Assert(ok, Equals, true)
	c.Assert(pubKey.Fingerprint, DeepEquals, testPrivKey1.PublicKey.Fingerprint)
}

func (dbs *databaseSuite) TestPublicKeyNotFound(c *C) {
	pk := asserts.OpenPGPPrivateKey(testPrivKey1)
	keyID := pk.PublicKey().ID()

	_, err := dbs.db.PublicKey("account0", keyID)
	c.Check(err, ErrorMatches, "no matching key pair found")

	err = dbs.db.ImportKey("account0", pk)
	c.Assert(err, IsNil)

	_, err = dbs.db.PublicKey("account0", "ff"+keyID)
	c.Check(err, ErrorMatches, "no matching key pair found")
}

type checkSuite struct {
	bs asserts.Backstore
	a  asserts.Assertion
}

var _ = Suite(&checkSuite{})

func (chks *checkSuite) SetUpTest(c *C) {
	var err error

	topDir := filepath.Join(c.MkDir(), "asserts-db")
	chks.bs, err = asserts.OpenFSBackstore(topDir)
	c.Assert(err, IsNil)

	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "0",
	}
	chks.a, err = asserts.AssembleAndSignInTest(asserts.TestOnlyType, headers, nil, asserts.OpenPGPPrivateKey(testPrivKey0))
	c.Assert(err, IsNil)
}

func (chks *checkSuite) TestCheckNoPubKey(c *C) {
	cfg := &asserts.DatabaseConfig{
		Backstore:      chks.bs,
		KeypairManager: asserts.NewMemoryKeypairManager(),
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)

	err = db.Check(chks.a)
	c.Assert(err, ErrorMatches, `no matching public key "[a-f0-9]+" for signature by "canonical"`)
}

func (chks *checkSuite) TestCheckExpiredPubKey(c *C) {
	trustedKey := testPrivKey0

	cfg := &asserts.DatabaseConfig{
		Backstore:      chks.bs,
		KeypairManager: asserts.NewMemoryKeypairManager(),
		TrustedKeys:    []*asserts.AccountKey{asserts.ExpiredAccountKeyForTest("canonical", &trustedKey.PublicKey)},
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)

	err = db.Check(chks.a)
	c.Assert(err, ErrorMatches, `assertion is signed with expired public key "[a-f0-9]+" from "canonical"`)
}

func (chks *checkSuite) TestCheckForgery(c *C) {
	trustedKey := testPrivKey0

	cfg := &asserts.DatabaseConfig{
		Backstore:      chks.bs,
		KeypairManager: asserts.NewMemoryKeypairManager(),
		TrustedKeys:    []*asserts.AccountKey{asserts.BootstrapAccountKeyForTest("canonical", &trustedKey.PublicKey)},
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)

	encoded := asserts.Encode(chks.a)
	content, encodedSig := chks.a.Signature()
	// forgery
	forgedSig := new(packet.Signature)
	forgedSig.PubKeyAlgo = testPrivKey1.PubKeyAlgo
	forgedSig.Hash = crypto.SHA256
	forgedSig.CreationTime = time.Now()
	forgedSig.IssuerKeyId = &testPrivKey0.KeyId
	h := crypto.SHA256.New()
	h.Write(content)
	err = forgedSig.Sign(h, testPrivKey1, &packet.Config{DefaultHash: crypto.SHA256})
	c.Assert(err, IsNil)
	buf := new(bytes.Buffer)
	forgedSig.Serialize(buf)
	forgedSigEncoded := "openpgp " + base64.StdEncoding.EncodeToString(buf.Bytes())
	forgedEncoded := bytes.Replace(encoded, encodedSig, []byte(forgedSigEncoded), 1)
	c.Assert(forgedEncoded, Not(DeepEquals), encoded)

	forgedAssert, err := asserts.Decode(forgedEncoded)
	c.Assert(err, IsNil)

	err = db.Check(forgedAssert)
	c.Assert(err, ErrorMatches, "failed signature verification: .*")
}

type signAddFindSuite struct {
	signingDB    *asserts.Database
	signingKeyID string
	db           *asserts.Database
}

var _ = Suite(&signAddFindSuite{})

func (safs *signAddFindSuite) SetUpTest(c *C) {
	cfg0 := &asserts.DatabaseConfig{
		KeypairManager: asserts.NewMemoryKeypairManager(),
	}
	db0, err := asserts.OpenDatabase(cfg0)
	c.Assert(err, IsNil)
	safs.signingDB = db0

	pk := asserts.OpenPGPPrivateKey(testPrivKey0)
	err = db0.ImportKey("canonical", pk)
	c.Assert(err, IsNil)
	safs.signingKeyID = pk.PublicKey().ID()

	topDir := filepath.Join(c.MkDir(), "asserts-db")
	bs, err := asserts.OpenFSBackstore(topDir)
	c.Assert(err, IsNil)

	trustedKey := testPrivKey0
	cfg := &asserts.DatabaseConfig{
		Backstore:      bs,
		KeypairManager: asserts.NewMemoryKeypairManager(),
		TrustedKeys:    []*asserts.AccountKey{asserts.BootstrapAccountKeyForTest("canonical", &trustedKey.PublicKey)},
	}
	db, err := asserts.OpenDatabase(cfg)
	c.Assert(err, IsNil)
	safs.db = db
}

func (safs *signAddFindSuite) TestSign(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Check(a1)
	c.Check(err, IsNil)
}

func (safs *signAddFindSuite) TestSignEmptyKeyID(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, "")
	c.Assert(err, ErrorMatches, "key id is empty")
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignMissingAuthorityId(c *C) {
	headers := map[string]string{
		"primary-key": "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `"authority-id" header is mandatory`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignMissingPrimaryKey(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `"primary-key" header is mandatory`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignNoPrivateKey(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, "abcd")
	c.Assert(err, ErrorMatches, "no matching key pair found")
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignUnknownType(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
	}
	a1, err := safs.signingDB.Sign(&asserts.AssertionType{Name: "xyz", PrimaryKey: nil}, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `internal error: unknown assertion type: "xyz"`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignNonPredefinedType(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
	}
	a1, err := safs.signingDB.Sign(&asserts.AssertionType{Name: "test-only", PrimaryKey: nil}, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `internal error: unpredefined assertion type for name "test-only" used.*`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignBadRevision(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
		"revision":     "zzz",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `"revision" header is not an integer: zzz`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestSignAssemblerError(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
		"count":        "zzz",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, ErrorMatches, `cannot assemble assertion test-only: "count" header is not an integer: zzz`)
	c.Check(a1, IsNil)
}

func (safs *signAddFindSuite) TestAddSuperseding(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Add(a1)
	c.Assert(err, IsNil)

	retrieved1, err := safs.db.Find(asserts.TestOnlyType, map[string]string{
		"primary-key": "a",
	})
	c.Assert(err, IsNil)
	c.Check(retrieved1, NotNil)
	c.Check(retrieved1.Revision(), Equals, 0)

	headers["revision"] = "1"
	a2, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Add(a2)
	c.Assert(err, IsNil)

	retrieved2, err := safs.db.Find(asserts.TestOnlyType, map[string]string{
		"primary-key": "a",
	})
	c.Assert(err, IsNil)
	c.Check(retrieved2, NotNil)
	c.Check(retrieved2.Revision(), Equals, 1)

	err = safs.db.Add(a1)
	c.Check(err, ErrorMatches, "revision 0 is older than current revision 1")
}

func (safs *signAddFindSuite) TestFindNotFound(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
	}
	a1, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Add(a1)
	c.Assert(err, IsNil)

	retrieved1, err := safs.db.Find(asserts.TestOnlyType, map[string]string{
		"primary-key": "b",
	})
	c.Assert(err, Equals, asserts.ErrNotFound)
	c.Check(retrieved1, IsNil)

	// checking also extra headers
	retrieved1, err = safs.db.Find(asserts.TestOnlyType, map[string]string{
		"primary-key":  "a",
		"authority-id": "other-auth-id",
	})
	c.Assert(err, Equals, asserts.ErrNotFound)
	c.Check(retrieved1, IsNil)
}

func (safs *signAddFindSuite) TestFindPrimaryLeftOut(c *C) {
	retrieved1, err := safs.db.Find(asserts.TestOnlyType, map[string]string{})
	c.Assert(err, ErrorMatches, "must provide primary key: primary-key")
	c.Check(retrieved1, IsNil)
}

func (safs *signAddFindSuite) TestFindMany(c *C) {
	headers := map[string]string{
		"authority-id": "canonical",
		"primary-key":  "a",
		"other":        "other-x",
	}
	aa, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)
	err = safs.db.Add(aa)
	c.Assert(err, IsNil)

	headers = map[string]string{
		"authority-id": "canonical",
		"primary-key":  "b",
		"other":        "other-y",
	}
	ab, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)
	err = safs.db.Add(ab)
	c.Assert(err, IsNil)

	headers = map[string]string{
		"authority-id": "canonical",
		"primary-key":  "c",
		"other":        "other-x",
	}
	ac, err := safs.signingDB.Sign(asserts.TestOnlyType, headers, nil, safs.signingKeyID)
	c.Assert(err, IsNil)
	err = safs.db.Add(ac)
	c.Assert(err, IsNil)

	res, err := safs.db.FindMany(asserts.TestOnlyType, map[string]string{
		"other": "other-x",
	})
	c.Assert(err, IsNil)
	c.Assert(res, HasLen, 2)
	primKeys := []string{res[0].Header("primary-key"), res[1].Header("primary-key")}
	sort.Strings(primKeys)
	c.Check(primKeys, DeepEquals, []string{"a", "c"})

	res, err = safs.db.FindMany(asserts.TestOnlyType, map[string]string{
		"other": "other-y",
	})
	c.Assert(err, IsNil)
	c.Assert(res, HasLen, 1)
	c.Check(res[0].Header("primary-key"), Equals, "b")

	res, err = safs.db.FindMany(asserts.TestOnlyType, map[string]string{})
	c.Assert(err, IsNil)
	c.Assert(res, HasLen, 3)

	res, err = safs.db.FindMany(asserts.TestOnlyType, map[string]string{
		"primary-key": "b",
		"other":       "other-y",
	})
	c.Assert(err, IsNil)
	c.Assert(res, HasLen, 1)

	res, err = safs.db.FindMany(asserts.TestOnlyType, map[string]string{
		"primary-key": "b",
		"other":       "other-x",
	})
	c.Assert(res, HasLen, 0)
	c.Check(err, Equals, asserts.ErrNotFound)
}

func (safs *signAddFindSuite) TestFindFindsTrustedAccountKeys(c *C) {
	pk1 := asserts.OpenPGPPrivateKey(testPrivKey1)
	pubKey1Encoded, err := asserts.EncodePublicKey(pk1.PublicKey())
	c.Assert(err, IsNil)

	now := time.Now().UTC()
	headers := map[string]string{
		"authority-id":           "canonical",
		"account-id":             "acc-id1",
		"public-key-id":          pk1.PublicKey().ID(),
		"public-key-fingerprint": pk1.PublicKey().Fingerprint(),
		"since":                  now.Format(time.RFC3339),
		"until":                  now.AddDate(1, 0, 0).Format(time.RFC3339),
	}
	accKey, err := safs.signingDB.Sign(asserts.AccountKeyType, headers, []byte(pubKey1Encoded), safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Add(accKey)
	c.Assert(err, IsNil)

	// find the trusted key as well
	tKey, err := safs.db.Find(asserts.AccountKeyType, map[string]string{
		"account-id":    "canonical",
		"public-key-id": safs.signingKeyID,
	})
	c.Assert(err, IsNil)
	c.Assert(tKey.(*asserts.AccountKey).AccountID(), Equals, "canonical")
	c.Assert(tKey.(*asserts.AccountKey).PublicKeyID(), Equals, safs.signingKeyID)

	// find trusted and untrusted
	accKeys, err := safs.db.FindMany(asserts.AccountKeyType, nil)
	c.Assert(err, IsNil)
	c.Check(accKeys, HasLen, 2)
}

func (safs *signAddFindSuite) TestDontLetAddConfusinglyAssertionClashingWithTrustedOnes(c *C) {
	// trusted
	pubKey0, err := safs.signingDB.PublicKey("canonical", safs.signingKeyID)
	c.Assert(err, IsNil)
	pubKey0Encoded, err := asserts.EncodePublicKey(pubKey0)
	c.Assert(err, IsNil)

	now := time.Now().UTC()
	headers := map[string]string{
		"authority-id":           "canonical",
		"account-id":             "canonical",
		"public-key-id":          safs.signingKeyID,
		"public-key-fingerprint": pubKey0.Fingerprint(),
		"since":                  now.Format(time.RFC3339),
		"until":                  now.AddDate(1, 0, 0).Format(time.RFC3339),
	}
	tKey, err := safs.signingDB.Sign(asserts.AccountKeyType, headers, []byte(pubKey0Encoded), safs.signingKeyID)
	c.Assert(err, IsNil)

	err = safs.db.Add(tKey)
	c.Check(err, ErrorMatches, `cannot add "account-key" assertion with primary key clashing with a trusted assertion: .*`)
}

type revisionErrorSuite struct{}

func (res *revisionErrorSuite) TestErrorText(c *C) {
	tests := []struct {
		err      error
		expected string
	}{
		// Invalid revisions.
		{&asserts.RevisionError{Used: -1}, "assertion revision is unknown"},
		{&asserts.RevisionError{Used: -100}, "assertion revision is unknown"},
		{&asserts.RevisionError{Current: -1}, "assertion revision is unknown"},
		{&asserts.RevisionError{Current: -100}, "assertion revision is unknown"},
		{&asserts.RevisionError{Used: -1, Current: -1}, "assertion revision is unknown"},
		// Used == Current.
		{&asserts.RevisionError{}, "revision 0 is already the current revision"},
		{&asserts.RevisionError{Used: 100, Current: 100}, "revision 100 is already the current revision"},
		// Used < Current.
		{&asserts.RevisionError{Used: 1, Current: 2}, "revision 1 is older than current revision 2"},
		{&asserts.RevisionError{Used: 2, Current: 100}, "revision 2 is older than current revision 100"},
		// Used > Current.
		{&asserts.RevisionError{Current: 1, Used: 2}, "revision 2 is more recent than current revision 1"},
		{&asserts.RevisionError{Current: 2, Used: 100}, "revision 100 is more recent than current revision 2"},
	}

	for _, test := range tests {
		c.Check(test.err, ErrorMatches, test.expected)
	}
}
