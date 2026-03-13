# TODO list

- Create a splitter that will split zip files and configure the writer to take a list of splitters. The splitters should detect be able to detect if they should split the block based on the first 1024 bytes of the block with an optional file name and content type. If the splitter doesn't recognize the format it should return an error and the writer should try the next splitter.