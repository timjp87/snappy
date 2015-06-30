// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2014-2015 Canonical Ltd
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

package i18n

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gosexy/gettext"
	. "gopkg.in/check.v1"
)

// Hook up check.v1 into the "go test" runner
func Test(t *testing.T) { TestingT(t) }

type i18nTestSuite struct {
}

var _ = Suite(&i18nTestSuite{})

func (s *i18nTestSuite) SetUpTest(c *C) {
	// this dir contains a special hand-crafted en_DK/snappy-test.mo
	// file
	localeDir, err := filepath.Abs("./mock-locale")
	c.Assert(err, IsNil)

	// this may fail on systems with no locale support (potentially
	// minimal build environments)
	gettext.BindTextdomain("snappy-test", localeDir)
	locale := gettext.SetLocale(gettext.LC_ALL, "en_DK.UTF-8")
	if locale != "en_DK.UTF-8" {
		c.Skip("can not init locale")
	}
	os.Setenv("LANGUAGE", "en_DK")

	// we use a custom test mo file
	TEXTDOMAIN = "snappy-test"
}

func (s *i18nTestSuite) TearDownTest(c *C) {
	gettext.SetLocale(gettext.LC_ALL, "")
}

func (s *i18nTestSuite) TestTranslatedSingular(c *C) {
	// no G() to avoid adding the test string to snappy-pot
	var Gtest = G
	c.Assert(Gtest("singular"), Equals, "translated singular")
}

func (s *i18nTestSuite) TestTranslatesPlural(c *C) {
	// no NG() to avoid adding the test string to snappy-pot
	var NGtest = NG
	c.Assert(NGtest("plural_1", "plural_2", 1), Equals, "translated plural_1")
}