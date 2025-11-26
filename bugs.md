# Security Vulnerabilities Report

This document outlines security vulnerabilities found in the Touka web framework.

## 1. Path Traversal in `fileserver.go`

**Vulnerability:** The `GetStaticDirHandleFunc` function in `fileserver.go` is vulnerable to path traversal attacks.

**Location:** `fileserver.go`

**Description:** The user-provided `filepath` parameter is directly used to construct the file path for the `http.FileServer`. An attacker could provide a malicious `filepath` (e.g., `../../../../etc/passwd`) to access arbitrary files on the system. While `path.Clean` is used in other parts of the file, it's not applied to the user-controlled `filepath` in this specific function.

**Recommendation:** Before using the `filepath` parameter, it should be sanitized to remove any directory traversal characters. The `path.Join` function can be used to safely construct the file path. Additionally, consider restricting file access to a specific base directory.

## 2. Cross-Site Scripting (XSS) in `context.go`

**Vulnerability:** The `String` method in `context.go` is vulnerable to Cross-Site Scripting (XSS) attacks.

**Location:** `context.go`

**Description:** The `String` method uses `fmt.Sprintf` to format the response, which does not perform any HTML escaping. If user-provided data is passed to this method, it could be rendered as HTML in the user's browser, allowing an attacker to execute arbitrary JavaScript.

**Recommendation:** When rendering user-provided content in an HTML context, use the `HTML` method instead of `String`. The `HTML` method uses the `html/template` package, which provides automatic escaping and mitigates XSS vulnerabilities. If the `String` method must be used, ensure that all user-provided data is properly escaped before being passed to it.

## 3. Potential Memory Safety Issues in `tree.go`

**Vulnerability:** The `StringToBytes` and `BytesToString` functions in `tree.go` use the `unsafe` package, which can lead to memory safety issues.

**Location:** `tree.go`

**Description:** These functions are used for performance optimizations, but they bypass Go's memory safety guarantees. If the underlying data is modified while a slice or string created with these functions is still in use, it can lead to memory corruption, crashes, or other unpredictable behavior.

**Recommendation:** While the use of `unsafe` is sometimes necessary for performance-critical code, it should be used with extreme caution. The code should be thoroughly reviewed to ensure that the underlying data is not modified in a way that could violate memory safety. Consider adding more detailed comments explaining why `unsafe` is used and what invariants must be maintained to ensure safety.

## 4. Information Disclosure in `context.go`

**Vulnerability:** The `FileText` and `SetRespBodyFile` methods in `context.go` can be used to expose arbitrary files on the system.

**Location:** `context.go`

**Description:** These methods take a file path as an argument and serve the contents of that file. While they use `filepath.Clean` to prevent simple path traversal attacks, they do not restrict file access to a specific directory. If an attacker can control the `filePath` parameter, they could use these methods to read any file on the system that the application has access to.

**Recommendation:** When serving files, it's important to restrict access to a specific, known directory. The application should have a configuration option for the base directory for static files, and all file access should be relative to that directory. The application should also ensure that the resolved path is still within the base directory.

## 5. Dependency Vulnerabilities

**Vulnerability:** The project's dependencies may contain known vulnerabilities.

**Location:** `go.mod`

**Description:** A dependency scan using `govulncheck` could not be completed due to a persistent crash. This means that the project may be using dependencies with known vulnerabilities.

**Recommendation:** The `govulncheck` tool should be run successfully to identify and remediate any vulnerabilities in the project's dependencies. The crash should be investigated and resolved.
