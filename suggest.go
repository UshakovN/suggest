package main

import (
  "io/ioutil"
  "log"
  stpb "main/proto/suggest/suggest_trie"
  "sort"
  "strings"

  "google.golang.org/protobuf/proto"
  "google.golang.org/protobuf/types/known/structpb"
)

type SuggestionTextBlock struct {
  Text      string `json:"text"`
  Highlight bool   `json:"hl"`
}

type SuggestAnswerItem struct {
  Weight     float32                `json:"weight"`
  Data       map[string]interface{} `json:"data"`
  TextBlocks []*SuggestionTextBlock `json:"text"`
}

type PaginatedSuggestResponse struct {
  Suggestions     []*SuggestAnswerItem `json:"suggestions"`
  PageNumber      int                  `json:"page_number"`
  TotalPagesCount int                  `json:"total_pages_count"`
  TotalItemsCount int                  `json:"total_items_count"`
}

type SuggestClassesItem struct {
  Classes []string
  Item    *stpb.Item
}

type ProtoTransformer struct {
  ItemsMap map[*Item]int
  Items    []*stpb.Item
}

func NewProtoTransformer() *ProtoTransformer {
  return &ProtoTransformer{
    ItemsMap: make(map[*Item]int),
  }
}

func (pt *ProtoTransformer) TransformTrie(builder *SuggestTrieBuilder) (*stpb.SuggestTrie, error) {
  trie := &stpb.SuggestTrie{}
  for _, d := range builder.Descendants {
    descendant, err := pt.TransformTrie(d.Builder)
    if err != nil {
      return nil, err
    }
    trie.DescendantKeys = append(trie.DescendantKeys, uint32(d.Key))
    trie.DescendantTries = append(trie.DescendantTries, descendant)
  }
  for _, suggest := range builder.SuggestItems {
    trieItems := &stpb.ClassItems{
      Class: suggest.Class,
    }
    for _, item := range suggest.Suggest {
      if _, ok := pt.ItemsMap[item.OriginalItem]; !ok {
        dataStruct, err := structpb.NewStruct(item.OriginalItem.Data)
        if err != nil {
          return nil, err
        }
        pt.ItemsMap[item.OriginalItem] = len(pt.Items)
        pt.Items = append(pt.Items, &stpb.Item{
          Weight:       item.OriginalItem.Weight,
          OriginalText: item.OriginalItem.OriginalText,
          Data:         dataStruct,
        })
      }
      trieItems.ItemWeights = append(trieItems.ItemWeights, item.Weight)
      trieItems.ItemIndexes = append(trieItems.ItemIndexes, uint32(pt.ItemsMap[item.OriginalItem]))
    }
    trie.Items = append(trie.Items, trieItems)
  }
  return trie, nil
}

func Transform(builder *SuggestTrieBuilder) (*stpb.SuggestData, error) {
  pt := NewProtoTransformer()
  trie, err := pt.TransformTrie(builder)
  if err != nil {
    return nil, err
  }
  return &stpb.SuggestData{
    Trie:  trie,
    Items: pt.Items,
  }, nil
}

func BuildSuggest(items []*Item, maxItemsPerPrefix int, postfixWeightFactor float32) (*stpb.SuggestData, error) {
  veroheadItemsCount := maxItemsPerPrefix * 2
  builder := &SuggestTrieBuilder{}
  for idx, item := range items {
    itemClasses := extractItemClasses(item)
    builder.Add(0, item.NormalizedText, veroheadItemsCount, &SuggestTrieItem{
      Weight:       item.Weight,
      OriginalItem: item,
    }, itemClasses)
    parts := strings.Split(item.NormalizedText, " ")
    for i := 1; i < len(parts); i++ {
      builder.Add(0, strings.Join(parts[i:], " "), veroheadItemsCount, &SuggestTrieItem{
        Weight:       item.Weight * postfixWeightFactor,
        OriginalItem: item,
      }, itemClasses)
    }
    if (idx+1)%100000 == 0 {
      log.Printf("addedd %d items of %d to suggest", idx+1, len(items))
    }
  }
  log.Printf("finalizing suggest")
  builder.Finalize(maxItemsPerPrefix)
  return Transform(builder)
}

func extractItemClasses(item *Item) []string {
  var classes []string
  // check duplicates
  seenClasses := map[string]bool{}
  // for backward compatibility
  if deprecatedClassInterface, ok := item.Data["class"]; ok {
    itemDeprecatedClass := LowerNormalizeString(deprecatedClassInterface.(string))
    classes = append(classes, itemDeprecatedClass)
    seenClasses[itemDeprecatedClass] = true
  }
  if classesInterface, ok := item.Data["classes"]; ok {
    itemClasses := classesInterface.([]interface{})
    for _, classInterface := range itemClasses {
      itemClass := LowerNormalizeString(classInterface.(string))
      if _, ok := seenClasses[itemClass]; !ok {
        classes = append(classes, itemClass)
        seenClasses[itemClass] = true
      }
    }
  }
  return classes
}

func doHighlight(originalPart string, originalSuggest string) []*SuggestionTextBlock {
  alphaLoweredPart := strings.ToLower(AlphaNormalizeString(originalPart))
  loweredSuggest := strings.ToLower(originalSuggest)

  partFields := strings.Fields(alphaLoweredPart)
  pos := 0
  var textBlocks []*SuggestionTextBlock
  for idx, partField := range partFields {
    suggestParts := strings.SplitN(loweredSuggest[pos:], partField, 2)
    if suggestParts[0] != "" {
      textBlocks = append(textBlocks, &SuggestionTextBlock{
        Text:      originalSuggest[pos : pos+len(suggestParts[0])],
        Highlight: false,
      })
    }
    textBlocks = append(textBlocks, &SuggestionTextBlock{
      Text:      originalSuggest[pos+len(suggestParts[0]) : pos+len(suggestParts[0])+len(partField)],
      Highlight: true,
    })
    if idx+1 == len(partFields) && len(suggestParts) == 2 && suggestParts[1] != "" {
      textBlocks = append(textBlocks, &SuggestionTextBlock{
        Text:      originalSuggest[pos+len(suggestParts[0])+len(partField) : pos+len(suggestParts[0])+len(partField)+len(suggestParts[1])],
        Highlight: false,
      })
    }
    pos += len(partField) + len(suggestParts[0])
  }
  return textBlocks
}

func GetSuggestItems(suggest *stpb.SuggestData, prefix []byte, requiredClasses, excludedClasses []string) []*stpb.Item {
  trie := suggest.Trie
  for _, c := range prefix {
    found := false
    for idx, key := range trie.DescendantKeys {
      if key != uint32(c) {
        continue
      }
      trie = trie.DescendantTries[idx]
      found = true
      break
    }
    if !found {
      return nil
    }
  }
  for len(trie.DescendantKeys) == 1 && len(trie.Items) == 0 {
    for _, d := range trie.DescendantTries {
      trie = d
      break
    }
  }
  var classesItems []*SuggestClassesItem
  classesItemsMap := map[uint32]int{}

  for _, suggestItems := range trie.Items {
    itemClass := suggestItems.Class
    for _, itemIndex := range suggestItems.ItemIndexes {
      if _, ok := classesItemsMap[itemIndex]; !ok {
        classesItemsMap[itemIndex] = len(classesItems)
        classesItems = append(classesItems, &SuggestClassesItem{
          Item: suggest.Items[itemIndex],
        })
      }
      itemIdx := classesItemsMap[itemIndex]
      classesItems[itemIdx].Classes = append(classesItems[itemIdx].Classes, itemClass)
    }
  }
  reqClassesMap := PrepareCheckMap(requiredClasses)
  exclClassesMap := PrepareCheckMap(excludedClasses)

  var items []*stpb.Item
  for _, classesItem := range classesItems {
    if itemSatisfyClasses(classesItem.Classes, reqClassesMap, exclClassesMap) {
      items = append(items, classesItem.Item)
    }
  }

  sort.Slice(items, func(i, j int) bool {
    return items[i].Weight > items[j].Weight
  })
  return items
}

func itemSatisfyClasses(classes []string, reqClassesMap, exclClassesMap map[string]bool) bool {
  var satisfyClass bool
  for _, class := range classes {
    if _, ok := reqClassesMap[class]; ok || len(reqClassesMap) == 0 {
      satisfyClass = true
      break
    }
  }
  for _, class := range classes {
    if _, ok := exclClassesMap[class]; ok {
      satisfyClass = false
      break
    }
  }
  return satisfyClass
}

func GetSuggest(suggest *stpb.SuggestData, originalPart string, normalizedPart string, requiredClasses, excludeClasses []string) []*SuggestAnswerItem {
  trieItems := GetSuggestItems(suggest, []byte(normalizedPart), requiredClasses, excludeClasses)
  items := make([]*SuggestAnswerItem, 0)
  if trieItems == nil {
    return items
  }
  for _, trieItem := range trieItems {
    items = append(items, &SuggestAnswerItem{
      Weight:     trieItem.Weight,
      Data:       trieItem.Data.AsMap(),
      TextBlocks: doHighlight(originalPart, trieItem.OriginalText),
    })
  }
  return items
}

func LoadSuggest(suggestDataPath string) (*stpb.SuggestData, error) {
  b, err := ioutil.ReadFile(suggestDataPath)
  if err != nil {
    return nil, err
  }
  suggestData := &stpb.SuggestData{}
  if err := proto.Unmarshal(b, suggestData); err != nil {
    return nil, err
  }
  return suggestData, nil
}
