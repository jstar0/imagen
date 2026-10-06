package imagen

import (
	"os"

	"golang.org/x/sys/windows"
)

func setTestDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func writePrivateTestFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	return setTestDACL(path, "D:P(A;;FA;;;"+user.User.Sid.String()+")")
}

func makeTestFilePublic(path string) error {
	return setTestDACL(path, "D:P(A;;FA;;;WD)")
}
