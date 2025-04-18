package viewmodel

import (
	"bytes"
	"fmt"
	"github.com/wagoodman/dive/cmd/dive/cli/internal/ui/v1"
	"github.com/wagoodman/dive/cmd/dive/cli/internal/ui/v1/format"
	"github.com/wagoodman/dive/internal/log"
	"regexp"
	"strings"

	"github.com/lunixbochs/vtclean"
	"github.com/wagoodman/dive/dive/filetree"
)

// FileTreeViewModel holds the UI objects and data models for populating the right pane. Specifically, the pane that
// shows selected layer or aggregate file ASCII tree.
type FileTreeViewModel struct {
	ModelTree *filetree.FileTree
	ViewTree  *filetree.FileTree
	RefTrees  []*filetree.FileTree
	comparer  filetree.Comparer

	constrainedRealEstate bool

	CollapseAll                 bool
	ShowAttributes              bool
	unconstrainedShowAttributes bool
	HiddenDiffTypes             []bool
	TreeIndex                   int
	bufferIndex                 int
	bufferIndexLowerBound       int

	refHeight int
	refWidth  int

	Buffer bytes.Buffer
}

// NewFileTreeViewModel creates a new view object attached the global [gocui] screen object.
func NewFileTreeViewModel(cfg v1.Config, initialLayer int) (treeViewModel *FileTreeViewModel, err error) {
	treeViewModel = new(FileTreeViewModel)

	comparer, err := cfg.TreeComparer()
	if err != nil {
		return nil, err
	}

	// populate main fields
	treeViewModel.ShowAttributes = cfg.Preferences.ShowFiletreeAttributes
	treeViewModel.unconstrainedShowAttributes = treeViewModel.ShowAttributes
	treeViewModel.CollapseAll = cfg.Preferences.CollapseFiletreeDirectory
	treeViewModel.ModelTree = cfg.Analysis.RefTrees[initialLayer]
	treeViewModel.RefTrees = cfg.Analysis.RefTrees
	treeViewModel.comparer = comparer
	treeViewModel.HiddenDiffTypes = make([]bool, 4)

	hiddenTypes := cfg.Preferences.FiletreeDiffHide
	for _, hType := range hiddenTypes {
		switch t := strings.ToLower(hType); t {
		case "added":
			treeViewModel.HiddenDiffTypes[filetree.Added] = true
		case "removed":
			treeViewModel.HiddenDiffTypes[filetree.Removed] = true
		case "modified":
			treeViewModel.HiddenDiffTypes[filetree.Modified] = true
		case "unmodified":
			treeViewModel.HiddenDiffTypes[filetree.Unmodified] = true
		default:
			return nil, fmt.Errorf("unknown diff.hide value: %s", t)
		}
	}

	return treeViewModel, treeViewModel.SetTreeByLayer(0, 0, initialLayer, initialLayer)
}

// Setup initializes the UI concerns within the context of a global [gocui] view object.
func (vm *FileTreeViewModel) Setup(lowerBound, height int) {
	vm.bufferIndexLowerBound = lowerBound
	vm.refHeight = height
}

// height returns the current height and considers the header
func (vm *FileTreeViewModel) height() int {
	if vm.ShowAttributes {
		return vm.refHeight - 1
	}
	return vm.refHeight
}

// bufferIndexUpperBound returns the current upper bounds for the view
func (vm *FileTreeViewModel) bufferIndexUpperBound() int {
	return vm.bufferIndexLowerBound + vm.height()
}

// IsVisible indicates if the file tree view pane is currently initialized
func (vm *FileTreeViewModel) IsVisible() bool {
	return vm != nil
}

// ResetCursor moves the cursor back to the top of the buffer and translates to the top of the buffer.
func (vm *FileTreeViewModel) ResetCursor() {
	vm.TreeIndex = 0
	vm.bufferIndex = 0
	vm.bufferIndexLowerBound = 0
}

// SetTreeByLayer populates the view model by stacking the indicated image layer file trees.
func (vm *FileTreeViewModel) SetTreeByLayer(bottomTreeStart, bottomTreeStop, topTreeStart, topTreeStop int) error {
	if topTreeStop > len(vm.RefTrees)-1 {
		return fmt.Errorf("invalid layer index given: %d of %d", topTreeStop, len(vm.RefTrees)-1)
	}
	newTree, err := vm.comparer.GetTree(filetree.NewTreeIndexKey(bottomTreeStart, bottomTreeStop, topTreeStart, topTreeStop))
	if err != nil {
		return fmt.Errorf("unable to fetch layer tree from cache: %w", err)
	}

	// preserve vm state on copy
	visitor := func(node *filetree.FileNode) error {
		newNode, err := newTree.GetNode(node.Path())
		if err == nil {
			newNode.Data.ViewInfo = node.Data.ViewInfo
		}
		return nil
	}
	err = vm.ModelTree.VisitDepthChildFirst(visitor, nil)
	if err != nil {
		return fmt.Errorf("unable to propagate layer tree: %w", err)
	}

	vm.ModelTree = newTree
	return nil
}

// CursorUp performs the internal view's buffer adjustments on cursor up. Note: this is independent of the gocui buffer.
func (vm *FileTreeViewModel) CursorUp() bool {
	if vm.TreeIndex <= 0 {
		return false
	}
	vm.TreeIndex--
	if vm.TreeIndex < vm.bufferIndexLowerBound {
		vm.bufferIndexLowerBound--
	}
	if vm.bufferIndex > 0 {
		vm.bufferIndex--
	}
	return true
}

// CursorDown performs the internal view's buffer adjustments on cursor down. Note: this is independent of the gocui buffer.
func (vm *FileTreeViewModel) CursorDown() bool {
	if vm.TreeIndex >= vm.ModelTree.VisibleSize() {
		return false
	}
	vm.TreeIndex++
	if vm.TreeIndex > vm.bufferIndexUpperBound() {
		vm.bufferIndexLowerBound++
	}
	vm.bufferIndex++
	if vm.bufferIndex > vm.height() {
		vm.bufferIndex = vm.height()
	}
	return true
}

func (vm *FileTreeViewModel) CurrentNode(filterRegex *regexp.Regexp) *filetree.FileNode {
	return vm.getAbsPositionNode(filterRegex)
}

// CursorLeft moves the cursor up until we reach the Parent Node or top of the tree
func (vm *FileTreeViewModel) CursorLeft(filterRegex *regexp.Regexp) error {
	var visitor func(*filetree.FileNode) error
	var evaluator func(*filetree.FileNode) bool
	var dfsCounter, newIndex int
	oldIndex := vm.TreeIndex
	currentNode := vm.getAbsPositionNode(filterRegex)

	if currentNode == nil {
		return nil
	}
	parentPath := currentNode.Parent.Path()

	visitor = func(curNode *filetree.FileNode) error {
		if strings.Compare(parentPath, curNode.Path()) == 0 {
			newIndex = dfsCounter
		}
		dfsCounter++
		return nil
	}

	evaluator = func(curNode *filetree.FileNode) bool {
		regexMatch := true
		if filterRegex != nil {
			match := filterRegex.Find([]byte(curNode.Path()))
			regexMatch = match != nil
		}
		return !curNode.Parent.Data.ViewInfo.Collapsed && !curNode.Data.ViewInfo.Hidden && regexMatch
	}

	err := vm.ModelTree.VisitDepthParentFirst(visitor, evaluator)
	if err != nil {
		return fmt.Errorf("unable to propagate tree on cursorLeft: %w", err)
	}

	vm.TreeIndex = newIndex
	moveIndex := oldIndex - newIndex
	if newIndex < vm.bufferIndexLowerBound {
		vm.bufferIndexLowerBound = vm.TreeIndex
	}

	if vm.bufferIndex > moveIndex {
		vm.bufferIndex -= moveIndex
	} else {
		vm.bufferIndex = 0
	}

	return nil
}

// CursorRight descends into directory expanding it if needed
func (vm *FileTreeViewModel) CursorRight(filterRegex *regexp.Regexp) error {
	node := vm.getAbsPositionNode(filterRegex)
	if node == nil {
		return nil
	}

	if !node.Data.FileInfo.IsDir {
		return nil
	}

	if len(node.Children) == 0 {
		return nil
	}

	if node.Data.ViewInfo.Collapsed {
		node.Data.ViewInfo.Collapsed = false
	}

	vm.TreeIndex++
	if vm.TreeIndex > vm.bufferIndexUpperBound() {
		vm.bufferIndexLowerBound++
	}

	vm.bufferIndex++
	if vm.bufferIndex > vm.height() {
		vm.bufferIndex = vm.height()
	}

	return nil
}

// PageDown moves to next page putting the cursor on top
func (vm *FileTreeViewModel) PageDown() error {
	nextBufferIndexLowerBound := vm.bufferIndexLowerBound + vm.height()
	nextBufferIndexUpperBound := nextBufferIndexLowerBound + vm.height()

	// todo: this work should be saved or passed to render...
	treeString := vm.ViewTree.StringBetween(nextBufferIndexLowerBound, nextBufferIndexUpperBound, vm.ShowAttributes)
	lines := strings.Split(treeString, "\n")

	newLines := len(lines) - 1
	if vm.height() >= newLines {
		nextBufferIndexLowerBound = vm.bufferIndexLowerBound + newLines
	}

	vm.bufferIndexLowerBound = nextBufferIndexLowerBound

	if vm.TreeIndex < nextBufferIndexLowerBound {
		vm.bufferIndex = 0
		vm.TreeIndex = nextBufferIndexLowerBound
	} else {
		vm.bufferIndex -= newLines
	}

	return nil
}

// PageUp moves to previous page putting the cursor on top
func (vm *FileTreeViewModel) PageUp() error {
	nextBufferIndexLowerBound := vm.bufferIndexLowerBound - vm.height()
	nextBufferIndexUpperBound := nextBufferIndexLowerBound + vm.height()

	// todo: this work should be saved or passed to render...
	treeString := vm.ViewTree.StringBetween(nextBufferIndexLowerBound, nextBufferIndexUpperBound, vm.ShowAttributes)
	lines := strings.Split(treeString, "\n")

	newLines := len(lines) - 2
	if vm.height() >= newLines {
		nextBufferIndexLowerBound = vm.bufferIndexLowerBound - newLines
	}

	vm.bufferIndexLowerBound = nextBufferIndexLowerBound

	if vm.TreeIndex > (nextBufferIndexUpperBound - 1) {
		vm.bufferIndex = 0
		vm.TreeIndex = nextBufferIndexLowerBound
	} else {
		vm.bufferIndex += newLines
	}
	return nil
}

// getAbsPositionNode determines the selected screen cursor's location in the file tree, returning the selected FileNode.
func (vm *FileTreeViewModel) getAbsPositionNode(filterRegex *regexp.Regexp) (node *filetree.FileNode) {
	var visitor func(*filetree.FileNode) error
	var evaluator func(*filetree.FileNode) bool
	var dfsCounter int

	visitor = func(curNode *filetree.FileNode) error {
		if dfsCounter == vm.TreeIndex {
			node = curNode
		}
		dfsCounter++
		return nil
	}

	evaluator = func(curNode *filetree.FileNode) bool {
		regexMatch := true
		if filterRegex != nil {
			match := filterRegex.Find([]byte(curNode.Path()))
			regexMatch = match != nil
		}
		parentCollapsed := false
		if curNode.Parent != nil {
			parentCollapsed = curNode.Parent.Data.ViewInfo.Collapsed
		}
		return !parentCollapsed && !curNode.Data.ViewInfo.Hidden && regexMatch
	}

	err := vm.ModelTree.VisitDepthParentFirst(visitor, evaluator)
	if err != nil {
		log.WithFields("error", err).Debug("unable to propagate tree on getAbsPositionNode")
	}

	return node
}

// ToggleCollapse will collapse/expand the selected FileNode.
func (vm *FileTreeViewModel) ToggleCollapse(filterRegex *regexp.Regexp) error {
	node := vm.getAbsPositionNode(filterRegex)
	if node != nil && node.Data.FileInfo.IsDir {
		node.Data.ViewInfo.Collapsed = !node.Data.ViewInfo.Collapsed
	}
	return nil
}

// ToggleCollapseAll will collapse/expand the all directories.
func (vm *FileTreeViewModel) ToggleCollapseAll() error {
	vm.CollapseAll = !vm.CollapseAll

	visitor := func(curNode *filetree.FileNode) error {
		curNode.Data.ViewInfo.Collapsed = vm.CollapseAll
		return nil
	}

	evaluator := func(curNode *filetree.FileNode) bool {
		return curNode.Data.FileInfo.IsDir
	}

	err := vm.ModelTree.VisitDepthChildFirst(visitor, evaluator)
	if err != nil {
		log.WithFields("error", err).Debug("unable to propagate tree on ToggleCollapseAll")
	}

	return nil
}

// ToggleSortOrder will toggle the sort order in which files are displayed
func (vm *FileTreeViewModel) ToggleSortOrder() error {
	vm.ModelTree.SortOrder = (vm.ModelTree.SortOrder + 1) % filetree.NumSortOrderConventions

	return nil
}

func (vm *FileTreeViewModel) ConstrainLayout() {
	if !vm.constrainedRealEstate {
		vm.constrainedRealEstate = true
		vm.unconstrainedShowAttributes = vm.ShowAttributes
		vm.ShowAttributes = false
	}
}

func (vm *FileTreeViewModel) ExpandLayout() {
	if vm.constrainedRealEstate {
		vm.ShowAttributes = vm.unconstrainedShowAttributes
		vm.constrainedRealEstate = false
	}
}

// ToggleAttributes will hi
func (vm *FileTreeViewModel) ToggleAttributes() error {
	// ignore any attempt to show the attributes when the layout is constrained
	if vm.constrainedRealEstate {
		return nil
	}
	vm.ShowAttributes = !vm.ShowAttributes
	return nil
}

// ToggleShowDiffType will show/hide the selected DiffType in the filetree pane.
func (vm *FileTreeViewModel) ToggleShowDiffType(diffType filetree.DiffType) {
	vm.HiddenDiffTypes[diffType] = !vm.HiddenDiffTypes[diffType]
}

// Update refreshes the state objects for future rendering.
func (vm *FileTreeViewModel) Update(filterRegex *regexp.Regexp, width, height int) error {
	vm.refWidth = width
	vm.refHeight = height

	// keep the vm selection in parity with the current DiffType selection
	err := vm.ModelTree.VisitDepthChildFirst(func(node *filetree.FileNode) error {
		node.Data.ViewInfo.Hidden = vm.HiddenDiffTypes[node.Data.DiffType]
		visibleChild := false
		for _, child := range node.Children {
			if !child.Data.ViewInfo.Hidden {
				visibleChild = true
				node.Data.ViewInfo.Hidden = false
			}
		}
		// hide nodes that do not match the current file filter regex (also don't unhide nodes that are already hidden)
		if filterRegex != nil && !visibleChild && !node.Data.ViewInfo.Hidden {
			match := filterRegex.FindString(node.Path())
			node.Data.ViewInfo.Hidden = len(match) == 0
		}
		return nil
	}, nil)

	if err != nil {
		return fmt.Errorf("unable to propagate vm model tree: %w", err)
	}

	// make a new tree with only visible nodes
	vm.ViewTree = vm.ModelTree.Copy()
	err = vm.ViewTree.VisitDepthParentFirst(func(node *filetree.FileNode) error {
		if node.Data.ViewInfo.Hidden {
			err1 := vm.ViewTree.RemovePath(node.Path())
			if err1 != nil {
				return err1
			}
		}
		return nil
	}, nil)

	if err != nil {
		return fmt.Errorf("unable to propagate vm view tree: %w", err)
	}

	return nil
}

// Render flushes the state objects (file tree) to the pane.
func (vm *FileTreeViewModel) Render() error {
	treeString := vm.ViewTree.StringBetween(vm.bufferIndexLowerBound, vm.bufferIndexUpperBound(), vm.ShowAttributes)
	lines := strings.Split(treeString, "\n")

	// update the contents
	vm.Buffer.Reset()
	for idx, line := range lines {
		if idx == vm.bufferIndex {
			_, err := fmt.Fprintln(&vm.Buffer, format.Selected(vtclean.Clean(line, false)))
			if err != nil {
				return err
			}
		} else {
			_, err := fmt.Fprintln(&vm.Buffer, line)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
